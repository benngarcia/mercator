package shadeform

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/capability/capacitytest"
)

// This file is Shadeform's half of the bounded CapacityProvider suite: the
// promises every backend keeps, asked of this one.
//
// The simulated half runs on every build, over real HTTP against an httptest
// server serving this package's in-memory marketplace, so what it exercises is
// the adapter's own request building, decoding, and reconciliation rather than a
// hand-written answer. The live half runs only when a credential is present,
// which is what keeps a red result here a fact about the contract rather than
// about whether this host holds an API key today.

// TestShadeformKeepsEveryCapacityPromiseAgainstItsOwnFake is the case CI runs.
// It needs no network beyond loopback and no credential.
func TestShadeformKeepsEveryCapacityPromiseAgainstItsOwnFake(t *testing.T) {
	marketplace := newFakeShadeform()
	marketplace.types = []instanceType{vmType()}
	adapter := adapterAgainst(t, marketplace)

	keepEveryPromise(t, adapter, "sfk01")
}

// adapterAgainst is this package's own marketplace served over a real socket.
// The adapter reaches it through its own transport rather than through the round
// tripper the rest of this package's cases inject, because the layer a unit test
// replaces is part of what a conformance case is for.
func adapterAgainst(t *testing.T, marketplace *fakeShadeform) *Adapter {
	t.Helper()
	server := httptest.NewServer(marketplace)
	t.Cleanup(server.Close)
	adapter, err := New("secret-key", map[string]string{
		"base_url":           server.URL + "/v1",
		"agent_download_url": testAgentDownloadURL,
	})
	if err != nil {
		t.Fatalf("build the adapter: %v", err)
	}
	adapter.client.backoff = 0
	return adapter
}

// TestAnAccountWhoseListingLagsIsLeftHoldingNothing is the outcome nobody knows,
// produced by the adapter that really produces it rather than described. The
// account registers each create and does not name it in the listing until the
// visibility scan has given up, so every provision here answers
// ErrCapacityIndeterminate with no machine named, which is the one case a
// receipt cannot reclaim.
//
// What the suite has to do is ask the account what it holds for the lease and
// destroy what it names. The promises are broken and that is the point: the
// machine still has to be given back, and this account keeps every instance it
// was ever asked to create, so a trial that lost one shows up as an instance
// nobody deleted.
func TestAnAccountWhoseListingLagsIsLeftHoldingNothing(t *testing.T) {
	marketplace := newFakeShadeform()
	marketplace.types = []instanceType{vmType()}
	// One look longer than the adapter's own visibility scan, so the create can
	// never see what it made and the next listing can.
	marketplace.listingLag = 4
	adapter := adapterAgainst(t, marketplace)
	subject := shadeformSubject(adapter, "sfl02")

	var outcomeUnknown bool
	for _, promise := range capacitytest.Promises() {
		if errors.Is(promise.Keep(t.Context(), subject), capability.ErrCapacityIndeterminate) {
			outcomeUnknown = true
		}
	}

	if !outcomeUnknown {
		t.Fatal("no provision came back with an outcome nobody knows, so nothing here was ever reclaimed from one")
	}
	if running := marketplace.stillRunning(); len(running) != 0 {
		t.Fatalf("the machines the lost answers allocated are still running: %v", running)
	}
}

// TestShadeformKeepsEveryCapacityPromiseAgainstTheLiveMarketplace rents real
// machines and gives them back. It is skipped, with the reason and the command,
// unless an operator has deliberately asked for it.
//
// Two gates rather than one, deliberately. The credential is what the suite
// cannot run without; the opt-in is because this case bills a real account, and
// an exported API key is not consent to rent a GPU inside `go test ./...`.
func TestShadeformKeepsEveryCapacityPromiseAgainstTheLiveMarketplace(t *testing.T) {
	credential := os.Getenv(liveCredentialEnv)
	switch {
	case credential == "":
		t.Skipf("no live provider: %s is unset. To run it: %s", liveCredentialEnv, liveCommand)
	case os.Getenv(liveOptInEnv) != "1":
		t.Skipf("a credential is present and renting a real machine is not implied by it: set %s=1. To run it: %s", liveOptInEnv, liveCommand)
	}
	adapter, err := New(credential, map[string]string{})
	if err != nil {
		t.Fatalf("build the live adapter: %v", err)
	}

	keepEveryPromise(t, adapter, "sfl01")
}

const (
	liveCredentialEnv = "SHADEFORM_API_KEY"
	liveOptInEnv      = "MERCATOR_SHADEFORM_LIVE"
	liveCommand       = "SHADEFORM_API_KEY=... MERCATOR_SHADEFORM_LIVE=1 go test ./internal/adapter/shadeform -run TestShadeformKeepsEveryCapacityPromiseAgainstTheLiveMarketplace"
)

// keepEveryPromise runs the shared suite against one Shadeform connection. The
// live and simulated cases share it so the two prove the same thing about the
// same code, which is the whole reason the suite is shared.
func keepEveryPromise(t *testing.T, provider capability.CapacityProvider, trialID string) {
	t.Helper()
	subject := shadeformSubject(provider, trialID)

	for _, promise := range capacitytest.Promises() {
		t.Run(promise.Name, func(t *testing.T) {
			err := promise.Keep(t.Context(), subject)
			if errors.Is(err, capacitytest.ErrNotApplicable) {
				t.Skip(err.Error())
			}
			if err != nil {
				t.Fatalf("%s (%s): %v", promise.Name, promise.Rule, err)
			}
		})
	}
}

// shadeformSubject is the identity every machine this package rents is held
// under, and the listing a promise rents from.
func shadeformSubject(provider capability.CapacityProvider, trialID string) capacitytest.Subject {
	lease := capacitytest.Lease{
		TrialID:      trialID,
		WorkspaceID:  "ws_capacity_conformance",
		ConnectionID: "conn_shadeform",
		// A machine this suite rents is expected to boot, fail to join, and be
		// given back, so the origin it is told to report to is one that answers
		// nothing and the material it would join with is material nothing minted.
		ControlPlaneURL: "https://reports.invalid",
		AgentVersion:    "v0.7.1",
		EnrollmentToken: "enrolment-nothing-minted",
		MaxLifetime:     30 * time.Minute,
	}
	return capacitytest.Subject{
		Name:     "shadeform",
		Provider: provider,
		Lease:    lease,
		Capacity: func(ctx context.Context) (capacitytest.Origin, error) {
			listing, err := capacitytest.Affordable(
				ctx,
				provider,
				capability.CapacityQuery{WorkspaceID: lease.WorkspaceID},
				maxTrialCostUSD,
				lease.MaxLifetime,
			)
			return capacitytest.OriginOf(listing), err
		},
	}
}

// maxTrialCostUSD bounds what one machine may cost over the whole trial. It is
// the same gate a launch trial applies to an offer, stated here because a
// capacity trial rents the machine rather than a workload on it.
const maxTrialCostUSD = 2.00
