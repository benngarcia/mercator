package shadeform

import (
	"context"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/capability"
)

func TestVerifyListsInstances(t *testing.T) {
	adapter := newTestAdapter(t, newFakeShadeform(), nil)

	if err := adapter.Verify(context.Background()); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	cases := map[string]map[string]string{
		"invalid shade_cloud":           {"shade_cloud": "sometimes"},
		"non-positive lifetime":         {"max_lifetime_hours": "0"},
		"non-numeric lifetime":          {"max_lifetime_hours": "abc"},
		"plaintext agent source":        {"agent_download_url": "http://downloads.test/agent-{version}"},
		"agent source pinning no build": {"agent_download_url": "https://downloads.test/agent-latest"},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New("k", config); err == nil {
				t.Errorf("%s must fail loudly", name)
			}
		})
	}
}

// TestAConnectionWithNoAgentSourceStillVerifies keeps the refusal where it
// belongs. A connection is built with empty configuration wherever Mercator
// states what a backend can do at all, so a constructor that demanded the agent
// source would take the whole catalog down; what it cannot do without one is
// rent a machine, and that is where ProvisionCapacity refuses.
func TestAConnectionWithNoAgentSourceStillVerifies(t *testing.T) {
	adapter := newTestAdapter(t, newFakeShadeform(), map[string]string{"agent_download_url": ""})

	if err := adapter.Verify(context.Background()); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestTheBootstrapScriptInstallsThePinnedAgentAndOpensNothing(t *testing.T) {
	script, err := bootstrapScript(bootstrap(), testAgentDownloadURL)

	if err != nil {
		t.Fatalf("render bootstrap: %v", err)
	}
	for _, want := range []string{
		"#!/bin/sh",
		"set -eu",
		"https://downloads.mercator.test/mercator-node/v0.7.1/linux-amd64",
		"install -m 0755",
		"chmod 0600 '/etc/mercator-node/bootstrap.env'",
		"ExecStart=/usr/local/bin/mercator-node",
		"Restart=always",
		"systemctl enable --now mercator-node.service",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("bootstrap script does not %q:\n%s", want, script)
		}
	}
	// A machine Mercator rents is reached only through the session its agent
	// opens outbound. Nothing here may publish a port or a runtime socket.
	for _, absent := range []string{"-H tcp://", "DOCKER_HOST", "docker.sock", "--listen", "sshd"} {
		if strings.Contains(script, absent) {
			t.Errorf("bootstrap script exposes %q:\n%s", absent, script)
		}
	}
}

func TestTheBootstrapRefusesMaterialAnUnattendedScriptCannotCarry(t *testing.T) {
	cases := map[string]capability.NodeBootstrap{
		"no control plane": {NodeID: "node_1", RentalID: "rent_1", EnrollmentToken: "enrol-token-1", AgentVersion: "v1"},
		"no identity":      {ControlPlaneURL: "https://mercator.test", RentalID: "rent_1", EnrollmentToken: "enrol-token-1", AgentVersion: "v1"},
		"no invitation":    {ControlPlaneURL: "https://mercator.test", NodeID: "node_1", RentalID: "rent_1", AgentVersion: "v1"},
		"no pinned build":  {ControlPlaneURL: "https://mercator.test", NodeID: "node_1", RentalID: "rent_1", EnrollmentToken: "enrol-token-1"},
		"token that ends the heredoc early": {
			ControlPlaneURL: "https://mercator.test", NodeID: "node_1", RentalID: "rent_1", AgentVersion: "v1",
			EnrollmentToken: "enrol-token-1\nMERCATOR_BOOTSTRAP_ENV\nrm -rf /",
		},
	}
	for name, invalid := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := bootstrapScript(invalid, testAgentDownloadURL)
			if err == nil {
				t.Fatal("want a refusal before a machine is paid for")
			}
			if strings.Contains(err.Error(), invalid.EnrollmentToken) && invalid.EnrollmentToken != "" {
				t.Fatalf("the refusal quotes the invitation: %v", err)
			}
		})
	}
}
