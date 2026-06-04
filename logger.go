package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"net/http"
	"os"
)

type closeFunc func() error

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			logger.Info(fmt.Sprintf("Served request: %s %s", r.Method, r.URL.Path))
		})
	}
}

func initialiseLogger(logFile string) (*slog.Logger, closeFunc, error) {
	handlers := []slog.Handler{
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}),
	}
	closer := func() error { return nil }

	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			return nil, nil, fmt.Errorf("could not open file: %v", err)
		}
		bufferedFile := bufio.NewWriterSize(f, 8192)

		infoHandler := slog.NewTextHandler(bufferedFile, &slog.HandlerOptions{
			Level: slog.LevelInfo,
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
