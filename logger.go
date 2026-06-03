package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

type closeFunc func() error

func requestLogger(logger *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			logger.Printf("Served request: %s %s", r.Method, r.URL.Path)
		})
	}
}

func initialiseLogger(logFile string) (*log.Logger, closeFunc, error) {
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			return nil, nil, fmt.Errorf("could not open file: %v", err)
		}
		bufferedFile := bufio.NewWriterSize(f, 8192)
		multiWriter := io.MultiWriter(os.Stderr, bufferedFile)

		close := func() error {
			err := bufferedFile.Flush()
			if err != nil {
				return err
			}
			err = f.Close()
			return err
		}

		return log.New(multiWriter, "", log.LstdFlags), close, nil
	}

	close := func() error {
		return nil
	}

	return log.New(os.Stderr, "", log.LstdFlags), close, nil
}
