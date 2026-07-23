package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/benngarcia/mercator/internal/conformanceprobe"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return conformanceprobe.Run(ctx, os.Args[1:], environment(), os.Stdout, os.Stderr)
}

func environment() map[string]string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	return values
}
