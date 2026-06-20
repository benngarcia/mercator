package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/bengarcia/mercator/internal/httpapi"
)

func main() {
	addr := env("MERCATOR_ADDR", ":8080")
	dsn := env("MERCATOR_SQLITE_DSN", "file:/data/mercator.db")
	handler, closeFn, err := httpapi.HandlerForSQLite(context.Background(), dsn, nil)
	if err != nil {
		log.Fatalf("start mercator: %v", err)
	}
	defer func() {
		if err := closeFn(); err != nil {
			log.Printf("close event log: %v", err)
		}
	}()
	log.Printf("mercator listening on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
