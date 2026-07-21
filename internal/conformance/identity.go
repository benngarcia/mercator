package conformance

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

type trialIdentity struct {
	trialID      string
	workspaceID  string
	connectionID string
}

func newTrialIdentity(adapterType string) (trialIdentity, error) {
	id, err := randomID("trial")
	if err != nil {
		return trialIdentity{}, err
	}
	suffix := strings.TrimPrefix(id, "trial_")
	return trialIdentity{trialID: id, workspaceID: "ws_" + suffix, connectionID: "conn_" + adapterType + "_" + suffix}, nil
}

func trialSecrets() (string, []byte, error) {
	token, err := randomSecret(32)
	if err != nil {
		return "", nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", nil, fmt.Errorf("generate trial master key: %w", err)
	}
	return token, key, nil
}

func randomID(prefix string) (string, error) {
	value, err := randomSecret(8)
	if err != nil {
		return "", err
	}
	return prefix + "_" + value, nil
}

func randomSecret(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate random value: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func cloneEnvironment(environment map[string]string) map[string]string {
	cloned := make(map[string]string, len(environment))
	for name, value := range environment {
		cloned[name] = value
	}
	return cloned
}
