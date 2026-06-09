package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"boot.dev/linko/internal/linkoerr"
	tint "github.com/lmittmann/tint"
	isatty "github.com/mattn/go-isatty"
	pkgerr "github.com/pkg/errors"
	"gopkg.in/natefinch/lumberjack.v2"
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
				slog.String("client_ip", redactIP(r.RemoteAddr)),
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

func initializeLogger(logFile string) (*slog.Logger, closeFunc, error) {
	var (
		handlers []slog.Handler
		closers  []closeFunc
	)

	handlers = append(handlers, tint.NewHandler(os.Stderr, &tint.Options{
		ReplaceAttr: replaceAttr,
		NoColor:     !checkTerminalIsATTY(),
	}))

	if logFile != "" {
		logger := &lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    1,
			MaxAge:     28,
			MaxBackups: 10,
			LocalTime:  false,
			Compress:   true,
		}
		handlers = append(handlers, slog.NewJSONHandler(logger, &slog.HandlerOptions{
			ReplaceAttr: replaceAttr,
		}))
		closers = append(closers, func() error {
			err := logger.Close()
			return err
		})
	}

	close := func() error {
		var errs []error
		for _, closer := range closers {
			errs = append(errs, closer())
		}
		return errors.Join(errs...)
	}
	return slog.New(slog.NewMultiHandler(handlers...)), close, nil
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
	if status == 401 || status == 403 || status == 500 {
		err = fmt.Errorf("%v", http.StatusText(status))
	}
	http.Error(w, err.Error(), status)
}

func checkTerminalIsATTY() bool {
	return isatty.IsCygwinTerminal(os.Stderr.Fd()) || isatty.IsTerminal(os.Stderr.Fd())
}

func redactIP(addr string) string {
	addrHost, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}

	ipAddr := net.ParseIP(addrHost).To4()
	if ipAddr == nil {
		return addr
	}

	return fmt.Sprintf("%d.%d.%d.x", ipAddr[0], ipAddr[1], ipAddr[2])
}
