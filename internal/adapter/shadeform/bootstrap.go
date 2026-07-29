package shadeform

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/benngarcia/mercator/internal/capability"
)

// This file is the whole of what a Shadeform machine is ever told. Shadeform
// runs a script launch configuration on the instance once it is active, and that
// script is the only channel Mercator has to a host it never opens a connection
// to: there is no inbound listener on the machine, no container runtime socket
// published off it, and no way in at all except the outbound session the agent
// opens for itself.
//
// What the script installs is the agent build the bootstrap pinned. The
// alternative, letting the machine resolve "latest" for itself, would make the
// build that enrolled a property of when a machine happened to boot, and the
// control plane records the version that actually ran precisely so the two can
// be compared.

const (
	agentBinaryPath = "/usr/local/bin/mercator-node"
	agentEnvPath    = "/etc/mercator-node/bootstrap.env"
	agentUnitPath   = "/etc/systemd/system/mercator-node.service"
	// agentVersionPlaceholder is what a connection's download URL puts the
	// pinned build in. It is required rather than optional: a URL that names no
	// version serves whatever is behind it today, and a machine that installed
	// that is running a build nobody pinned.
	agentVersionPlaceholder = "{version}"
)

// bootstrapScript is what Shadeform runs on the machine. It fetches the pinned
// agent, writes the identity the agent enrolls under, and starts it under
// systemd so a crashed agent comes back on a machine nobody can reach.
//
// The material it writes down is the short-lived invitation and nothing else.
// The token names one node identity and one generation, it is redeemable once,
// and it is spent the moment the agent redeems it, so what stays on the disk
// afterwards opens no door. No Mercator API token, no provider credential, and
// no registry account is ever on this machine.
func bootstrapScript(bootstrap capability.NodeBootstrap, downloadTemplate string) (string, error) {
	source, err := agentSource(downloadTemplate, bootstrap.AgentVersion)
	if err != nil {
		return "", err
	}
	environment, err := bootstrapEnvironment(bootstrap)
	if err != nil {
		return "", err
	}
	return strings.NewReplacer(
		"__AGENT_URL__", source,
		"__AGENT_BINARY__", agentBinaryPath,
		"__AGENT_ENV__", agentEnvPath,
		"__AGENT_UNIT__", agentUnitPath,
		"__AGENT_ENVIRONMENT__", environment,
	).Replace(bootstrapTemplate), nil
}

// encodedBootstrapScript is that script as Shadeform's create body carries it.
func encodedBootstrapScript(bootstrap capability.NodeBootstrap, downloadTemplate string) (string, error) {
	script, err := bootstrapScript(bootstrap, downloadTemplate)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString([]byte(script)), nil
}

// agentSource is where this machine fetches its agent from, with the pinned
// build substituted into the connection's own URL.
//
// There is no default. Mercator publishes no agent binary today, so a URL
// guessed here would be a paid machine fetching a 404 and never enrolling, and
// the Run would wait out its whole enrolment patience to learn it. A connection
// that states none is refused before any instance is created.
func agentSource(downloadTemplate, agentVersion string) (string, error) {
	template := strings.TrimSpace(downloadTemplate)
	if template == "" {
		return "", fmt.Errorf(
			"shadeform: this connection states no agent_download_url, and a machine with no agent on it is capacity nothing can execute on",
		)
	}
	if strings.TrimSpace(agentVersion) == "" {
		return "", fmt.Errorf("shadeform: the bootstrap pins no agent version, so nothing states which build this machine should install")
	}
	source := strings.ReplaceAll(template, agentVersionPlaceholder, agentVersion)
	if err := unattendedValue("agent_download_url", source); err != nil {
		return "", err
	}
	return source, nil
}

// bootstrapEnvironment is the identity the agent reads at start, in the
// environment file its unit loads. The agent takes each of these as a flag too;
// the file keeps the invitation out of the process table, where every user on
// the machine could read it.
func bootstrapEnvironment(bootstrap capability.NodeBootstrap) (string, error) {
	values := []struct{ name, value string }{
		{"MERCATOR_CONTROL_PLANE_URL", bootstrap.ControlPlaneURL},
		{"MERCATOR_NODE_ID", bootstrap.NodeID},
		{"MERCATOR_RENTAL_ID", bootstrap.RentalID},
		{"MERCATOR_NODE_GENERATION", strconv.FormatUint(bootstrap.Generation, 10)},
		{"MERCATOR_NODE_ENROLLMENT_TOKEN", bootstrap.EnrollmentToken},
	}
	lines := make([]string, 0, len(values))
	for _, value := range values {
		if err := unattendedValue(value.name, value.value); err != nil {
			return "", err
		}
		lines = append(lines, value.name+"="+value.value)
	}
	return strings.Join(lines, "\n"), nil
}

// unattendedValue refuses a value this script cannot carry, and never says what
// the value was: one of them is a credential, and a refusal that quoted it would
// put the credential in the Run's failure.
//
// The bar is one line of printable ASCII with no spaces. Every value here is
// Mercator's own (a URL, two identities, a generation, and a minted token), so
// anything outside that is a bug upstream rather than an operator's input, and
// the machine is refused before it is paid for rather than handed a heredoc that
// ends early or a unit file with a second directive in it.
func unattendedValue(name, value string) error {
	if value == "" {
		return fmt.Errorf("shadeform: bootstrap %s is empty, and a machine cannot be told who it is without it", name)
	}
	for _, character := range value {
		if character <= ' ' || character > '~' {
			return fmt.Errorf("shadeform: bootstrap %s carries a character no unattended script can be handed safely", name)
		}
	}
	return nil
}

// bootstrapTemplate is a POSIX shell script because that is the floor every
// shade_os image meets. It installs a systemd unit rather than backgrounding the
// agent itself: the agent has to survive a reboot of a machine nobody can log
// into to restart it.
const bootstrapTemplate = `#!/bin/sh
# Mercator node bootstrap, run by Shadeform once this machine is active.
#
# It installs the pinned node agent and starts it. The agent connects outbound to
# the control plane and executes the workloads it is given there. This machine
# listens on nothing, publishes no container runtime socket, and holds no
# credential but the single-use invitation below.
set -eu
umask 077

# Every directory this script writes into, rather than the two it invents. A
# machine whose image ships no /usr/local/bin or /etc/systemd/system is a machine
# the install would fail on halfway, having already fetched the agent.
install -d -m 0755 /usr/local/bin /etc/systemd/system
install -d -m 0700 /etc/mercator-node /var/lib/mercator-node

download="$(mktemp)"
curl --fail --silent --show-error --location --retry 5 --retry-connrefused \
	--output "$download" '__AGENT_URL__'
install -m 0755 "$download" '__AGENT_BINARY__'
rm -f "$download"

cat > '__AGENT_ENV__' <<'MERCATOR_BOOTSTRAP_ENV'
__AGENT_ENVIRONMENT__
MERCATOR_BOOTSTRAP_ENV
chmod 0600 '__AGENT_ENV__'

cat > '__AGENT_UNIT__' <<'MERCATOR_UNIT'
[Unit]
Description=Mercator node agent
Wants=network-online.target
After=network-online.target docker.service

[Service]
Type=simple
EnvironmentFile=__AGENT_ENV__
ExecStart=__AGENT_BINARY__
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
MERCATOR_UNIT

systemctl daemon-reload
systemctl enable --now mercator-node.service
`
