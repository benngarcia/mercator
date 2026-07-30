package shadeform

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/dockertest"
)

// This file is the higher-fidelity half of the bootstrap: the script Mercator
// hands a rented machine, run by a real shell on a real filesystem.
//
// Everything that matters about the claim needs one. Whether the quoted heredocs
// survive a token with shell metacharacters in it, whether the environment file
// really lands at 0600, whether the unit file arrives with the directives
// systemd needs, and whether the whole thing runs under `set -eu` without
// tripping on its own error handling are answers a string comparison cannot
// give. What Shadeform does with the script is beyond reach here; what the
// script does when something runs it is not.
//
// The two commands the script cannot be allowed to really perform are stubbed:
// curl, because there is no agent binary to fetch and no machine to fetch it to,
// and systemctl, because a container is not a booted host. Both record what they
// were asked for, so the case asserts on the ask rather than on the effect.
//
// To exercise it by hand, an operator runs the same thing this case does:
//
//	docker run --rm --mount type=bind,src=$STUBS,dst=/stub,readonly busybox:1.37 \
//	  sh -c 'PATH=/stub:$PATH sh /stub/bootstrap.sh'

// bootstrapHostImage is a minimal userland with a POSIX shell, coreutils-alikes
// and nothing else. The script is written to the floor every shade_os image
// meets, so proving it against a smaller userland than a real host proves more
// rather than less.
const bootstrapHostImage = "busybox:1.37"

func TestTheBootstrapScriptRunsOnARealMachine(t *testing.T) {
	requireBootstrapHost(t)
	stubs := t.TempDir()
	script, err := bootstrapScript(bootstrap(), testAgentDownloadURL)
	if err != nil {
		t.Fatalf("render bootstrap: %v", err)
	}
	write(t, filepath.Join(stubs, "bootstrap.sh"), script)
	// curl writes the file it was asked to fetch, so the install that follows has
	// something to install, and records the URL it was given.
	write(t, filepath.Join(stubs, "curl"), `#!/bin/sh
echo "$@" >> /tmp/curl.args
while [ $# -gt 0 ]; do
	if [ "$1" = "--output" ]; then
		printf 'pinned-agent-payload' > "$2"
	fi
	shift
done
`)
	write(t, filepath.Join(stubs, "systemctl"), `#!/bin/sh
echo "$@" >> /tmp/systemctl.args
`)

	report := runInContainer(t, stubs, `
		PATH=/stub:$PATH sh /stub/bootstrap.sh
		echo "--- agent"
		cat /usr/local/bin/mercator-node
		echo ""
		stat -c %a /usr/local/bin/mercator-node
		echo "--- env-mode"
		stat -c %a /etc/mercator-node/bootstrap.env
		echo "--- env"
		cat /etc/mercator-node/bootstrap.env
		echo "--- unit"
		cat /etc/systemd/system/mercator-node.service
		echo "--- curl"
		cat /tmp/curl.args
		echo "--- systemctl"
		cat /tmp/systemctl.args
	`)

	for _, want := range []string{
		// The agent the connection pinned, fetched and installed executable.
		"pinned-agent-payload",
		"https://downloads.mercator.test/mercator-node/v0.7.1/linux-amd64",
		"--- agent\npinned-agent-payload\n755",
		// The identity, in a file only root can read.
		"--- env-mode\n600",
		"MERCATOR_CONTROL_PLANE_URL=https://mercator.test",
		"MERCATOR_NODE_ID=node_1",
		"MERCATOR_RENTAL_ID=rent_1",
		"MERCATOR_NODE_GENERATION=1",
		"MERCATOR_NODE_ENROLLMENT_TOKEN=enrol-token-1",
		// A unit that comes back on its own, on a machine nobody can log into.
		"ExecStart=/usr/local/bin/mercator-node",
		"EnvironmentFile=/etc/mercator-node/bootstrap.env",
		"Restart=always",
		"daemon-reload",
		"enable --now mercator-node.service",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the bootstrapped machine does not show %q:\n%s", want, report)
		}
	}
}

// requireBootstrapHost holds this machine's daemon and makes sure the userland
// the script runs in is here.
func requireBootstrapHost(t *testing.T) {
	t.Helper()
	dockertest.Exclusive(t)
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no reachable Docker daemon to run a bootstrap on: %v", err)
	}
	if exec.Command("docker", "image", "inspect", bootstrapHostImage).Run() == nil {
		return
	}
	if output, err := exec.Command("docker", "pull", "--quiet", bootstrapHostImage).CombinedOutput(); err != nil {
		t.Skipf("this machine can neither pull %s nor already hold it: %v\n%s", bootstrapHostImage, err, output)
	}
}

func runInContainer(t *testing.T, stubs, command string) string {
	t.Helper()
	output, err := exec.Command("docker", "run", "--rm",
		"--mount", "type=bind,src="+stubs+",dst=/stub,readonly",
		bootstrapHostImage, "sh", "-c", command,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("run the bootstrap on a real machine: %v\n%s", err, output)
	}
	return string(output)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
