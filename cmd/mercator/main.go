package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/bengarcia/mercator/internal/cli"
	"github.com/bengarcia/mercator/internal/httpapi"
)

func main() {
	os.Exit(run(context.Background(), os.Args, environ(), os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, env map[string]string, stdout, stderr io.Writer) int {
	if len(args) > 1 && args[1] != "serve" {
		return cli.Run(ctx, cli.Config{
			BaseURL: envValue(env, "MERCATOR_API_URL", ""),
			Args:    args[1:],
			Stdout:  stdout,
			Stderr:  stderr,
		})
	}
	addr := envValue(env, "MERCATOR_ADDR", ":8080")
	dsn := envValue(env, "MERCATOR_SQLITE_DSN", "file:/data/mercator.db")
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
	return 0
}

func environ() map[string]string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		for i, char := range entry {
			if char == '=' {
				values[entry[:i]] = entry[i+1:]
				break
			}
		}
	}
	return values
}

func envValue(values map[string]string, key, fallback string) string {
	if value := values[key]; value != "" {
		return value
	}
	return fallback
}
