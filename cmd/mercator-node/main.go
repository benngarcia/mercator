// Command mercator-node is the Mercator node agent. It runs on a reusable
// machine, connects outbound to the control plane, and executes the workloads
// it is given there, one after another.
//
// It listens on nothing and exposes no container runtime socket. Its identity
// and its short-lived enrollment token arrive through the bootstrap the
// capacity provider delivered, or through the environment for a machine an
// operator enrolls by hand.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/benngarcia/mercator/internal/nodeagent"
)

// version is stamped at build time and reported at enrollment, so the control
// plane records the build that actually ran rather than the one the bootstrap
// asked for.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mercator-node: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	settings, err := parse()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: settings.level}))
	state, err := nodeagent.OpenState(filepath.Join(settings.stateDir, "node-state.json"), settings.identity.NodeID)
	if err != nil {
		return err
	}
	agent := nodeagent.New(
		settings.identity,
		nodeagent.NewDockerRuntime(settings.dockerBinary),
		nodeagent.NewHTTPTransport(settings.identity.ControlPlaneURL, nil),
		state,
		nodeagent.WithLogger(logger),
		nodeagent.WithHeartbeat(settings.heartbeat),
	)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.InfoContext(ctx, "node agent starting",
		"node_id", settings.identity.NodeID,
		"rental_id", settings.identity.RentalID,
		"generation", settings.identity.Generation,
		"control_plane", settings.identity.ControlPlaneURL,
		"version", version,
	)
	return agent.Run(ctx)
}

type settings struct {
	identity     nodeagent.Identity
	stateDir     string
	dockerBinary string
	heartbeat    time.Duration
	level        slog.Level
}

// parse reads the agent's configuration. Every identity field is required and
// none has a default: a node that guesses which node it is could enroll as
// another one, so a missing value fails loudly instead.
func parse() (settings, error) {
	parsed := settings{heartbeat: 20 * time.Second, level: slog.LevelInfo}
	flag.StringVar(&parsed.identity.ControlPlaneURL, "control-plane", os.Getenv("MERCATOR_CONTROL_PLANE_URL"), "Control plane base URL to connect outbound to")
	flag.StringVar(&parsed.identity.NodeID, "node-id", os.Getenv("MERCATOR_NODE_ID"), "This node's immutable identity")
	flag.StringVar(&parsed.identity.RentalID, "rental-id", os.Getenv("MERCATOR_RENTAL_ID"), "The Rental this node belongs to")
	generation := flag.String("generation", os.Getenv("MERCATOR_NODE_GENERATION"), "The Rental generation this node was provisioned for")
	flag.StringVar(&parsed.identity.EnrollmentToken, "enrollment-token", os.Getenv("MERCATOR_NODE_ENROLLMENT_TOKEN"), "Short-lived, single-use enrollment credential")
	flag.StringVar(&parsed.stateDir, "state-dir", envOr("MERCATOR_NODE_STATE_DIR", "/var/lib/mercator-node"), "Directory for the agent's durable local state")
	flag.StringVar(&parsed.dockerBinary, "docker", envOr("MERCATOR_NODE_DOCKER", "docker"), "Docker CLI binary")
	flag.DurationVar(&parsed.heartbeat, "heartbeat", parsed.heartbeat, "How often to report host and inventory facts")
	verbose := flag.Bool("verbose", false, "Log at debug level")
	flag.Parse()

	if *verbose {
		parsed.level = slog.LevelDebug
	}
	parsed.identity.AgentVersion = version
	if *generation != "" {
		parsedGeneration, err := strconv.ParseUint(*generation, 10, 64)
		if err != nil {
			return settings{}, fmt.Errorf("generation must be a positive integer, got %q", *generation)
		}
		parsed.identity.Generation = parsedGeneration
	}
	for name, value := range map[string]string{
		"control-plane":    parsed.identity.ControlPlaneURL,
		"node-id":          parsed.identity.NodeID,
		"rental-id":        parsed.identity.RentalID,
		"enrollment-token": parsed.identity.EnrollmentToken,
	} {
		if value == "" {
			return settings{}, fmt.Errorf("--%s is required", name)
		}
	}
	if parsed.identity.Generation == 0 {
		return settings{}, fmt.Errorf("--generation is required")
	}
	return parsed, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
