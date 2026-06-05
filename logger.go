package main

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"boot.dev/linko/internal/linkoerr"
	pkgerr "github.com/pkg/errors"
)

type closeFunc func() error

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

type multiError interface {
	error
	Unwrap() []error
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			logger.Info("Served request",
				"method", r.Method,
				"path", r.URL.Path,
				"client_ip", r.RemoteAddr)
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
