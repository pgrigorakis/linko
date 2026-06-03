package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

func requestLogger(logger *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			logger.Printf("Served request: %s %s", r.Method, r.URL.Path)
		})
	}
}

func initialiseLogger() (*log.Logger, error) {
	fileName := os.Getenv("LINKO_LOG_FILE")
	if fileName != "" {
		f, err := os.OpenFile(fileName, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("could not open file: %v", err)
		}
		multiWriter := io.MultiWriter(os.Stderr, f)
		return log.New(multiWriter, "", log.LstdFlags), nil
	}

	return log.New(os.Stderr, "", log.LstdFlags), nil
}
