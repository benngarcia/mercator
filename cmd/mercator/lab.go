package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/benngarcia/mercator/internal/lab"
	"github.com/benngarcia/mercator/internal/scenario"
	"github.com/benngarcia/mercator/internal/webauth"
)

type labServeOptions struct {
	blueprint string
	addr      string
	seed      string
	policy    string
}

func runLabCommand(ctx context.Context, args []string, env map[string]string, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		_, _ = fmt.Fprintln(stderr, labUsage)
		return 2
	}
	if args[2] == "help" || args[2] == "-h" || args[2] == "--help" {
		_, _ = fmt.Fprintln(stdout, labUsage)
		return 0
	}
	switch args[2] {
	case "author":
		return runLabAuthor(args[3:], stdout, stderr)
	case "generate":
		return runLabGenerate(args[3:], stdout, stderr)
	case "run":
		return runLabExecute(ctx, args[3:], stdout, stderr)
	case "replay":
		return runLabReplay(ctx, args[3:], stdout, stderr)
	case "minimize":
		return runLabMinimize(ctx, args[3:], stdout, stderr)
	case "promote":
		return runLabPromote(ctx, args[3:], stdout, stderr)
	case "serve":
	default:
		_, _ = fmt.Fprintln(stderr, labUsage)
		return 2
	}
	options, err := parseLabServeOptions(args)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	return runLabServe(ctx, options, env)
}

const labUsage = `usage: mercator lab <command>

Commands:
  author    Write a valid Blueprint template
  generate  Generate a deterministic Blueprint
  run       Execute a Blueprint and write one .mlab
  replay    Reconstruct a run from one .mlab
  minimize  Shrink a replayable failure
  promote   Prove and promote a target Blueprint
  serve     Run the isolated Lab server and production console`

func parseLabServeOptions(args []string) (labServeOptions, error) {
	if len(args) < 3 || args[1] != "lab" || args[2] != "serve" {
		return labServeOptions{}, errors.New("usage: mercator lab serve --blueprint FILE [--addr LOOPBACK:PORT] [--seed SEED] [--policy NAME]")
	}
	flags := flag.NewFlagSet("mercator lab serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := labServeOptions{}
	flags.StringVar(&options.blueprint, "blueprint", "", "Blueprint path")
	flags.StringVar(&options.addr, "addr", "127.0.0.1:8081", "loopback listen address")
	flags.StringVar(&options.seed, "seed", "", "keyed entropy seed override")
	flags.StringVar(&options.policy, "policy", "default", "policy identity")
	if err := flags.Parse(args[3:]); err != nil {
		return labServeOptions{}, fmt.Errorf("usage: mercator lab serve --blueprint FILE [--addr LOOPBACK:PORT] [--seed SEED] [--policy NAME]: %w", err)
	}
	if flags.NArg() != 0 {
		return labServeOptions{}, errors.New("usage: mercator lab serve --blueprint FILE [--addr LOOPBACK:PORT] [--seed SEED] [--policy NAME]")
	}
	if options.blueprint == "" {
		return labServeOptions{}, errors.New("mercator lab serve: --blueprint is required")
	}
	if options.policy == "" {
		return labServeOptions{}, errors.New("mercator lab serve: --policy cannot be empty")
	}
	if !isLoopback(options.addr) {
		return labServeOptions{}, fmt.Errorf("mercator lab serve: --addr must be loopback, got %s", options.addr)
	}
	return options, nil
}

func runLabServe(ctx context.Context, options labServeOptions, env map[string]string) int {
	blueprint, err := scenario.LoadBlueprint(options.blueprint)
	if err != nil {
		log.Printf("load Lab Blueprint: %v", err)
		return 1
	}
	tape, samples, err := lab.Compile(blueprint, lab.CompileOptions{Seed: options.seed})
	if err != nil {
		log.Printf("compile Lab Blueprint: %v", err)
		return 1
	}
	token, generated, err := apiTokenFromEnv(env)
	if err != nil {
		log.Printf("create Lab operator token: %v", err)
		return 1
	}
	localAuth, err := webauth.NewLocal(localDeveloperEmail)
	if err != nil {
		log.Printf("configure Lab browser session: %v", err)
		return 1
	}
	server, err := lab.NewServer(ctx, lab.ServerConfig{
		Execution: lab.Config{
			Blueprint:        blueprint,
			Tape:             tape,
			Samples:          samples,
			Limits:           lab.DefaultLimits(),
			Policy:           options.policy,
			MercatorRevision: currentRevision(),
		},
		OperatorToken: token,
		WebAuth:       localAuth,
	})
	if err != nil {
		log.Printf("configure Lab server: %v", err)
		return 1
	}
	listener, err := net.Listen("tcp", options.addr)
	if err != nil {
		_ = server.Shutdown(context.Background())
		log.Printf("listen: %v", err)
		return 1
	}
	if generated {
		log.Printf("generated Lab operator token: %s", token)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	log.Printf("Mercator Lab %q listening on http://%s", blueprint.Name, options.addr)
	exitCode := 0
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("serve Lab: %v", err)
			exitCode = 1
		}
	case <-ctx.Done():
	case received := <-stop:
		log.Printf("received %s; shutting down Lab", received)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown Lab: %v", err)
		return 1
	}
	return exitCode
}

func currentRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "development"
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value
		}
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "development"
}
