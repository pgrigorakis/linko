package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"boot.dev/linko/internal/linkoerr"
	pkgerr "github.com/pkg/errors"
)

const logContextKey contextKey = "log_context"

type LogContext struct {
	Username string
	Error    error
}

type closeFunc func() error

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

type multiError interface {
	error
	Unwrap() []error
}

type spyReadCloser struct {
	io.ReadCloser
	bytesRead int
}

type spyResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
	statusCode   int
}

func (r *spyReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytesRead += n
	return n, err
}

func (w *spyResponseWriter) Write(p []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytesWritten += n
	return n, err
}

func (w *spyResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			spyReader := &spyReadCloser{ReadCloser: r.Body}
			r.Body = spyReader
			spyWriter := &spyResponseWriter{ResponseWriter: w}

			logCtx := &LogContext{}
			ctx := context.WithValue(r.Context(), logContextKey, logCtx)
			next.ServeHTTP(spyWriter, r.WithContext(ctx))

			attrs := []any{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("client_ip", r.RemoteAddr),
				slog.Duration("duration", time.Since(start)),
				slog.Int("request_body_bytes", spyReader.bytesRead),
				slog.Int("response_status", spyWriter.statusCode),
				slog.Int("response_body_bytes", spyWriter.bytesWritten),
				slog.String("request_id", spyWriter.Header().Get("X-Request-ID")),
			}

			if logCtx.Username != "" {
				attrs = append(attrs, slog.String("user", logCtx.Username))
			}

			if logCtx.Error != nil {
				attrs = append(attrs, slog.Any("error", logCtx.Error))
			}

			logger.Info("Served request", attrs...)
		})
	}
}

func requestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			xRequestIdHeader := r.Header.Get("X-Request-ID")

			if xRequestIdHeader == "" {
				xRequestIdHeader = rand.Text()
			}

			w.Header().Set("X-Request-ID", xRequestIdHeader)
			next.ServeHTTP(w, r)
		})
	}
}

func initialiseLogger(logFile string) (*slog.Logger, closeFunc, error) {
	handlers := []slog.Handler{
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level:       slog.LevelDebug,
			ReplaceAttr: replaceAttr}),
	}
	closer := func() error { return nil }

	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			return nil, nil, fmt.Errorf("could not open file: %v", err)
		}
		bufferedFile := bufio.NewWriterSize(f, 8192)

		infoHandler := slog.NewJSONHandler(bufferedFile, &slog.HandlerOptions{
			Level:       slog.LevelInfo,
			ReplaceAttr: replaceAttr,
		})
		handlers = append(handlers, infoHandler)

		closer = func() error {
			err := bufferedFile.Flush()
			if err != nil {
				return err
			}
			err = f.Close()
			return err
		}
	}

	logger := slog.New(slog.NewMultiHandler(handlers...))

	return logger, closer, nil
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}

		if multiErr, ok := a.Value.Any().(multiError); ok {
			var errAttrs []slog.Attr
			for i, err := range multiErr.Unwrap() {
				stdErrorWithAttrs := getErrorAttrs(err)
				numberedError := slog.GroupAttrs(
					fmt.Sprintf("error_%d", i+1),
					stdErrorWithAttrs...,
				)
				errAttrs = append(errAttrs, numberedError)
			}
			return slog.GroupAttrs("errors", errAttrs...)
		}

		stdErrorWithAttrs := getErrorAttrs(err)
		return slog.GroupAttrs("error", stdErrorWithAttrs...)
	}
	return a
}

func getErrorAttrs(err error) []slog.Attr {
	stdError := []slog.Attr{{
		Key:   "message",
		Value: slog.StringValue(err.Error()),
	}}
	stdErrorWithAttrs := append(stdError, linkoerr.Attrs(err)...)

	if stackErr, ok := errors.AsType[stackTracer](err); ok {
		stackTraceErr := slog.Attr{
			Key:   "stack_trace",
			Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
		}
		stdErrorWithAttrs = append(stdErrorWithAttrs, stackTraceErr)
	}

	return stdErrorWithAttrs
}

func httpError(ctx context.Context, w http.ResponseWriter, status int, err error) {
	if logCtx, ok := ctx.Value(logContextKey).(*LogContext); ok {
		logCtx.Error = err
	}
	http.Error(w, err.Error(), status)
}
