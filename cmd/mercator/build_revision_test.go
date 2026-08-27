package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReleaseBuildReportsTheLinkerStampedRevision(t *testing.T) {
	const revision = "release-build-sentinel"
	binary := filepath.Join(t.TempDir(), "mercator")
	build := exec.CommandContext(t.Context(), "../../scripts/build-mercator.sh", binary)
	build.Env = append(os.Environ(), "MERCATOR_BUILD_REVISION="+revision)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build release binary: %v\n%s", err, output)
	}

	port := freePort(t)
	serve := exec.Command(binary, "serve")
	serve.Env = append(os.Environ(),
		fmt.Sprintf("MERCATOR_ADDR=127.0.0.1:%d", port),
		"MERCATOR_API_TOKEN=operator-token",
		"MERCATOR_SECRET_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"MERCATOR_SQLITE_DSN=file:"+filepath.Join(t.TempDir(), "mercator.db"),
	)
	if err := serve.Start(); err != nil {
		t.Fatalf("start release binary: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- serve.Wait() }()
	t.Cleanup(func() {
		if serve.Process != nil {
			_ = serve.Process.Signal(os.Interrupt)
		}
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("release binary did not stop after interrupt")
		}
	})

	url := fmt.Sprintf("http://127.0.0.1:%d/health/ready", port)
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := http.Get(url)
		if err == nil {
			defer func() { _ = response.Body.Close() }()
			var readiness struct {
				BuildRevision string `json:"build_revision"`
			}
			if err := json.NewDecoder(response.Body).Decode(&readiness); err != nil {
				t.Fatalf("decode release readiness: %v", err)
			}
			if readiness.BuildRevision != revision {
				t.Fatalf("release build revision = %q, want %q", readiness.BuildRevision, revision)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("release binary did not become ready: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
