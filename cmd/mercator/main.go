package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/benngarcia/mercator/internal/cli"
	"github.com/benngarcia/mercator/internal/conformance"
	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/keymaterial"
	"github.com/benngarcia/mercator/internal/tlsmaterial"
	"github.com/benngarcia/mercator/internal/webauth"
)

const localDeveloperEmail = "developer@localhost"

type serveOptions struct {
	localAuthEmail string
}

func main() {
	os.Exit(run(context.Background(), os.Args, environ(), os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, env map[string]string, stdout, stderr io.Writer) int {
	if len(args) > 2 && args[1] == "help" && args[2] == "verify" {
		return conformance.RunCommand(ctx, []string{"--help"}, env, stdout, stderr)
	}
	if len(args) > 1 && args[1] == "verify" {
		return conformance.RunCommand(ctx, args[2:], env, stdout, stderr)
	}
	if len(args) > 1 && args[1] == "lab" {
		return runLabCommand(ctx, args, env, stdout, stderr)
	}
	if len(args) > 1 && args[1] == "rekey" {
		return runRekeyCommand(ctx, env, stdout, stderr)
	}
	if len(args) > 1 && args[1] == "backup" {
		return runBackupCommand(ctx, args, env, stdout, stderr)
	}
	if len(args) > 1 && args[1] != "serve" {
		return cli.Run(ctx, cli.Config{
			BaseURL:     envValue(env, "MERCATOR_API_URL", ""),
			Token:       envValue(env, "MERCATOR_API_TOKEN", ""),
			WorkspaceID: envValue(env, "MERCATOR_WORKSPACE_ID", ""),
			ConfigPath:  cli.DefaultConfigPath(env),
			Args:        args[1:],
			Stdout:      stdout,
			Stderr:      stderr,
		})
	}
	options, err := parseServeOptions(args)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	addr := envValue(env, "MERCATOR_ADDR", "127.0.0.1:8080")
	if options.localAuthEmail != "" && !isLoopback(addr) {
		stdlog.Printf("configure local login: --dev requires a loopback MERCATOR_ADDR, got %s", addr)
		return 1
	}
	apiToken, generatedToken, err := apiTokenFromEnv(env)
	if err != nil {
		stdlog.Printf("load api token: %v", err)
		return 1
	}
	if generatedToken {
		stdlog.Printf("generated MERCATOR_API_TOKEN for this server process: %s", apiToken)
	}
	webauthCfg, err := webauth.FromEnv(env)
	if err != nil {
		stdlog.Printf("configure OIDC login: %v", err)
		return 1
	}
	if options.localAuthEmail != "" && webauthCfg.Enabled() {
		stdlog.Printf("configure local login: --dev cannot be combined with MERCATOR_OIDC_*")
		return 1
	}
	masterKey, err := masterKeyFromEnv(env)
	if err != nil {
		stdlog.Printf("load secret key: %v", err)
		return 1
	}
	tlsFiles, err := tlsmaterial.FromEnv(func(name string) string { return env[name] })
	if err != nil {
		stdlog.Printf("configure TLS: %v", err)
		return 1
	}
	// A non-loopback listener with no certificate would put bearer tokens and
	// run data on the wire in the clear. That used to be a warning followed by
	// serving anyway; it is now a refusal, because a warning in a startup log is
	// not a security control.
	if !isLoopback(addr) && !tlsFiles.Configured() {
		stdlog.Printf("configure TLS: MERCATOR_ADDR %s is not loopback and no TLS material is configured; set %s and %s, or bind a loopback address", addr, tlsmaterial.CertFileVar, tlsmaterial.KeyFileVar)
		return 1
	}
	// Creating a tenant, inviting a machine and forcing a sink to deliver are
	// not operations the audience of the public API has any business reaching.
	// A deployment that is reachable beyond this host must therefore say where
	// those answer instead, and there is no address they answer on by default.
	announced, err := announcedHost(env[publicURLVar])
	if err != nil {
		stdlog.Printf("configure the public URL: %v", err)
		return 1
	}
	adminAddr := env[adminAddrVar]
	if exposure := publicExposure(addr, announced); exposure != "" && adminAddr == "" {
		stdlog.Printf("configure the administrative listener: %s, so %s must name a private address for workspace creation, node invitation and sink delivery", exposure, adminAddrVar)
		return 1
	}
	listeners, err := bindListeners(addr, adminAddr)
	if err != nil {
		stdlog.Printf("listen: %v", err)
		return 1
	}
	dsn, err := sqliteDSN(env)
	if err != nil {
		listeners.close()
		stdlog.Printf("resolve database path: %v", err)
		return 1
	}
	// One process owns one database. Holding the claim for as long as this
	// server serves is what makes `mercator rekey` refuse to rotate the master
	// key underneath it.
	claim, err := claimDatabase(dsn)
	if err != nil {
		listeners.close()
		stdlog.Printf("claim database: %v", err)
		return 1
	}
	defer claim.release()
	runtime, err := daemon.New(ctx, daemon.Config{
		SQLiteDSN:      dsn,
		OperatorToken:  apiToken,
		MasterKey:      masterKey,
		TLS:            tlsFiles,
		AdminAddr:      listeners.adminAddress(),
		PublicURL:      env[publicURLVar],
		Getenv:         func(name string) string { return env[name] },
		WebAuth:        webauthCfg,
		LocalAuthEmail: options.localAuthEmail,
		// The node agent build every invitation asks for. A capacity provider
		// substitutes it into the download URL an operator hosts the binary at, so
		// it is the operator's statement of which build that URL serves and nothing
		// here can guess it. A deployment that states none provisions no machine.
		AgentVersion: env["MERCATOR_AGENT_VERSION"],
	})
	if err != nil {
		listeners.close()
		stdlog.Printf("configure server: %v", err)
		return 1
	}
	// A loopback broker holding a token only this process knows is unusable
	// until the CLI learns it. Write it down rather than making the operator
	// copy it out of the log.
	// The address the kernel gave is what a client can reach. They differ
	// whenever the operator asked for port 0, and announcing the asked-for
	// address then names a port nothing is listening on.
	baseURL := listenURL(tlsFiles, listeners.public.Addr().String())
	if generatedToken && isLoopback(addr) {
		shareLocalContext(env, baseURL, apiToken)
	}
	serveErr := listeners.serve(runtime)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	stdlog.Printf("mercator listening on %s", baseURL)
	if listeners.admin != nil {
		stdlog.Printf("mercator administrative operations listening on %s", listenURL(tlsFiles, listeners.adminAddress()))
	}
	exitCode := 0
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			stdlog.Printf("serve: %v", err)
			exitCode = 1
		}
	case sig := <-stop:
		stdlog.Printf("received %s; shutting down", sig)
	case <-ctx.Done():
		// The runtime's background work is built on this context, so a
		// cancelled one has already stopped reconciling. Serving on past that
		// point would be serving from a control plane that had stopped
		// controlling anything.
		stdlog.Printf("context cancelled; shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		stdlog.Printf("shutdown: %v", err)
		return 1
	}
	return exitCode
}

func parseServeOptions(args []string) (serveOptions, error) {
	if len(args) <= 2 {
		return serveOptions{}, nil
	}
	if len(args) == 3 && args[1] == "serve" && args[2] == "--dev" {
		return serveOptions{localAuthEmail: localDeveloperEmail}, nil
	}
	return serveOptions{}, fmt.Errorf("usage: mercator serve [--dev]")
}

// adminAddrVar names the private address the administrative operations answer
// on. It is required whenever this deployment is reachable beyond this host.
const adminAddrVar = "MERCATOR_ADMIN_ADDR"

// publicURLVar is the base URL this deployment is reachable at from outside.
// Nodes dial it and workloads report to it, and an operator behind a proxy or a
// tunnel sets it while binding loopback.
const publicURLVar = "MERCATOR_PUBLIC_URL"

// publicExposure names the reason this deployment answers to something other
// than this machine, and is empty when it does not. Two facts say so, and only
// the operator holds the second one: a bind address that is not loopback says
// it outright, and the host a public URL announces says it through whatever
// proxy or tunnel is forwarding to a loopback bind, which the listening socket
// cannot see.
//
// The reason is a sentence rather than a boolean because it is what the refusal
// tells the operator, and a refusal that cannot say which fact tripped it sends
// them to the wrong variable.
func publicExposure(addr, announced string) string {
	if !isLoopback(addr) {
		return fmt.Sprintf("MERCATOR_ADDR %s is not loopback", addr)
	}
	if announced != "" {
		return fmt.Sprintf("%s %s announces this deployment at an address that is not this machine", publicURLVar, announced)
	}
	return ""
}

// announcedHost is the host MERCATOR_PUBLIC_URL announces this deployment at.
// It is empty when the variable is unset and when the URL names loopback, which
// is a developer pointing reporting at their own process rather than an
// exposure.
//
// Anything else that is not a URL a node could dial is a startup failure rather
// than an empty host. Every reader of this variable needs an absolute URL: the
// orchestrator hands it to each workload verbatim as MERCATOR_REPORT_URL and
// webauth appends "/auth/callback" to it for the OIDC redirect. Answering
// "announces nothing" for a value like "mercator.example.com" or the one-slash
// "https:/mercator.example.com" would exempt exactly the proxied deployment this
// check exists to catch, so the two ways of writing an unusable URL both refuse.
func announcedHost(publicURL string) (string, error) {
	trimmed := strings.TrimSpace(publicURL)
	if trimmed == "" {
		return "", nil
	}
	unusable := fmt.Errorf("%s must be an absolute http:// or https:// URL naming a host, got %q", publicURLVar, trimmed)
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: %s", unusable, err)
	}
	host := parsed.Hostname()
	if host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", unusable
	}
	if host == "localhost" || net.ParseIP(host).IsLoopback() {
		return "", nil
	}
	return host, nil
}

// serverListeners are the sockets this process answers on. One http.Server
// serves both, so one Shutdown drains both, and the two differ only in which
// operations are routed on them.
type serverListeners struct {
	public net.Listener
	admin  net.Listener
}

func bindListeners(publicAddr, adminAddr string) (serverListeners, error) {
	public, err := net.Listen("tcp", publicAddr)
	if err != nil {
		return serverListeners{}, err
	}
	if adminAddr == "" {
		return serverListeners{public: public}, nil
	}
	admin, err := net.Listen("tcp", adminAddr)
	if err != nil {
		_ = public.Close()
		return serverListeners{}, err
	}
	bound := serverListeners{public: public, admin: admin}
	if err := requireOneInterface(admin.Addr()); err != nil {
		bound.close()
		return serverListeners{}, err
	}
	return bound, nil
}

// requireOneInterface refuses an administrative listener bound to the wildcard.
// A surface reachable on every interface this machine has is not a private one,
// and the request's local address is what tells the two listeners apart, which
// a wildcard bind makes unanswerable.
func requireOneInterface(addr net.Addr) error {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("%s must name a TCP address, got %s", adminAddrVar, addr)
	}
	if tcp.IP.IsUnspecified() {
		return fmt.Errorf("%s must name one interface rather than the wildcard, got %s", adminAddrVar, addr)
	}
	return nil
}

func (l serverListeners) close() {
	if l.public != nil {
		_ = l.public.Close()
	}
	if l.admin != nil {
		_ = l.admin.Close()
	}
}

// adminAddress is the bound address administrative operations answer on, which
// is what the server compares each request's local address against. Empty when
// this deployment runs one listener.
func (l serverListeners) adminAddress() string {
	if l.admin == nil {
		return ""
	}
	return l.admin.Addr().String()
}

func (l serverListeners) serve(runtime *daemon.Runtime) <-chan error {
	served := make(chan error, 2)
	go func() { served <- runtime.Serve(l.public) }()
	if l.admin != nil {
		go func() { served <- runtime.Serve(l.admin) }()
	}
	return served
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// listenURL is the base URL a client reaches this server on. The scheme follows
// the material: a process holding a certificate terminates TLS itself, so
// telling anyone http:// would be telling them the wrong thing.
func listenURL(material tlsmaterial.Material, addr string) string {
	if material.Configured() {
		return "https://" + addr
	}
	return "http://" + addr
}

// shareLocalContext hands this machine's CLI the base URL and token of the
// server just started. Failing to write it is not fatal: the token is already
// in the log above, so the operator can still export it by hand.
func shareLocalContext(env map[string]string, baseURL, token string) {
	path := cli.DefaultConfigPath(env)
	changed, err := cli.WriteLocalContext(path, baseURL, token)
	if err != nil {
		stdlog.Printf("could not write the %q CLI context (%v); export MERCATOR_API_TOKEN instead", cli.LocalContextName, err)
		return
	}
	if changed {
		stdlog.Printf("wrote the %q CLI context to %s; mercator commands on this machine need no further setup", cli.LocalContextName, path)
	}
}

// masterKeyFromEnv reads the process master key, which is required. Credential
// sealing, run-report tokens and node identity each derive a subkey from it,
// and each of those derivations answers an absent master key by disabling
// itself. Returning no key here therefore used to start a server with three
// security features silently off, which is why an absent key is now a startup
// failure naming the variable.
func masterKeyFromEnv(values map[string]string) ([]byte, error) {
	raw := values["MERCATOR_SECRET_KEY"]
	if raw == "" {
		return nil, errors.New("MERCATOR_SECRET_KEY is required (32+ decoded bytes, hex or base64)")
	}
	return keymaterial.Decode("MERCATOR_SECRET_KEY", raw, 32)
}

func apiTokenFromEnv(values map[string]string) (string, bool, error) {
	if token := values["MERCATOR_API_TOKEN"]; token != "" {
		return token, false, nil
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", false, err
	}
	return hex.EncodeToString(bytes), true, nil
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

// sqliteDSN resolves where the event log lives. An operator who names a path
// gets exactly that path. Everyone else gets a per-user data directory, which
// this creates, because a server that cannot start until you invent a database
// location is a server nobody can try. The container image sets the variable
// explicitly, so it keeps its own /data volume.
func sqliteDSN(env map[string]string) (string, error) {
	if dsn := env["MERCATOR_SQLITE_DSN"]; dsn != "" {
		return dsn, nil
	}
	base := env["XDG_DATA_HOME"]
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("no MERCATOR_SQLITE_DSN and no home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(base, "mercator")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return "file:" + filepath.Join(dir, "mercator.db"), nil
}

func envValue(values map[string]string, key, fallback string) string {
	if value := values[key]; value != "" {
		return value
	}
	return fallback
}
