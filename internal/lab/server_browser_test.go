package lab

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/scenario"
	"github.com/benngarcia/mercator/internal/webauth"
	"github.com/benngarcia/mercator/web"
)

func TestLabConsoleUsesNormalAPIAndSSE(t *testing.T) {
	if os.Getenv("MERCATOR_BROWSER_TEST") != "1" {
		t.Skip("set MERCATOR_BROWSER_TEST=1 to run the Lab console acceptance flow")
	}
	if file, err := web.Static().Open("index.html"); err != nil {
		t.Fatal("embedded console is not built; run bun run build in web/app first")
	} else {
		_ = file.Close()
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve Lab browser test path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	blueprintPath := filepath.Join(
		repoRoot,
		"internal", "scenario", "scenarios", "demos", "artifact-warmth-restart.json",
	)
	sidecarPath := filepath.Join(
		repoRoot,
		"internal", "scenario", "scenarios", "demos", "artifact-warmth-restart.ui.json",
	)
	blueprint, err := scenario.LoadBlueprint(blueprintPath)
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile Blueprint: %v", err)
	}
	localAuth, err := webauth.NewLocal("developer@localhost")
	if err != nil {
		t.Fatalf("configure local browser session: %v", err)
	}
	server, err := NewServer(context.Background(), ServerConfig{
		Execution: Config{
			Blueprint:        blueprint,
			Tape:             tape,
			Samples:          samples,
			Limits:           DefaultLimits(),
			Policy:           "default",
			MercatorRevision: "browser-test",
		},
		OperatorToken: "lab-browser-token",
		WebAuth:       localAuth,
	})
	if err != nil {
		t.Fatalf("open Lab server: %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown Lab browser server: %v", err)
		}
	})
	output := os.Getenv("MERCATOR_BROWSER_OUTPUT")
	if output == "" {
		output = filepath.Join(repoRoot, "output", "playwright-lab")
	}
	command := exec.Command("bun", "run", filepath.Join("test", "browser", "lab-console.mjs"))
	command.Dir = filepath.Join(repoRoot, "web", "app")
	command.Env = append(os.Environ(),
		"MERCATOR_BROWSER_BASE_URL="+httpServer.URL,
		"MERCATOR_BROWSER_OUTPUT="+output,
		"MERCATOR_BROWSER_UI_SIDECAR="+sidecarPath,
		"MERCATOR_LAB_TOKEN=lab-browser-token",
	)
	result, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run Lab browser acceptance: %v\n%s\nartifacts: %s", err, result, output)
	}
	bundlePath := filepath.Join(output, "artifact-warmth-restart.mlab")
	archive, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read browser Run Bundle: %v", err)
	}
	bundle, err := DecodeRunBundle(archive)
	if err != nil {
		t.Fatalf("decode browser Run Bundle: %v", err)
	}
	report, err := VerifyVerticalProof(context.Background(), bundle)
	if err != nil {
		t.Fatalf(
			"verify browser Run Bundle: %v\nReplay: mercator lab replay --bundle %s",
			err,
			bundlePath,
		)
	}
	t.Logf(
		"Lab browser acceptance: %s15 checkpoints passed; normalized output %s",
		result,
		report.NormalizedSHA256,
	)
}
