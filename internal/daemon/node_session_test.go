package daemon_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/daemon"
)

// TestAMachineGoesOnWorkingAfterItsFirstSessionCredentialLapses is the highest
// fidelity this claim can be made at short of a rented machine: a real node agent
// process, over the real node protocol, against this daemon's own HTTP server and
// its own registry, with the session window shortened so the lapse happens inside
// a test rather than half an hour into a production day.
//
// Before session renewal existed this case could not pass, and no case in the
// tree could fail: every one of them finished inside the thirty minutes a
// credential is good for. An agent whose credential lapsed answered by replaying
// the invitation it joined with, which this registry spent when it was redeemed,
// so the machine stopped being able to speak about thirty minutes after
// bootstrapping and went on running containers nobody could reach.
//
// Three things are asserted and each is read from a different place. The
// renewals are counted off the wire, so they are exchanges that really happened
// rather than something the agent says about itself. The machine is still
// placeable and still executes what it is given, which is the whole point of a
// credential that keeps working. And it enrolled once, which is what says it
// renewed rather than rejoining.
func TestAMachineGoesOnWorkingAfterItsFirstSessionCredentialLapses(t *testing.T) {
	fleet := startFleet(t, renewingEvery(2*time.Second))

	waitFor(t, func() bool { return fleet.renewals.Load() >= 2 }, "the agent never renewed its session twice, so nothing outlived one credential")
	runID := fleet.submitRun(t)
	fleet.completeWorkload(t, runID, 0)

	if enrolled := fleet.enrolments.Load(); enrolled != 1 {
		t.Fatalf("the machine redeemed an invitation %d times, and a machine that renews its session joins the fleet once", enrolled)
	}
}

// TestNothingAnOperatorCanReadCarriesTheInvitationAMachineJoinedWith is the leak
// half. The bootstrap credential is returned exactly once, in the answer to the
// invitation that minted it, and after that it must appear in nothing an operator
// can ask this control plane for.
//
// It is a byte scan rather than a field check on purpose. A field check asserts
// that the fields somebody thought about are clean, and the defect this guards
// against is material reaching the record through a field nobody thought about:
// an error string carrying the request that failed, a debug payload, a projection
// that copied a whole command.
func TestNothingAnOperatorCanReadCarriesTheInvitationAMachineJoinedWith(t *testing.T) {
	fleet := startFleet(t, renewingEvery(2*time.Second))
	waitFor(t, func() bool { return fleet.renewals.Load() >= 2 }, "the agent never renewed its session twice, so nothing outlived one credential")
	runID := fleet.submitRun(t)
	fleet.completeWorkload(t, runID, 0)

	for _, path := range []string{
		"/v1/runs/" + runID + "/events?workspace_id=" + daemon.DefaultWorkspaceID,
		"/v1/nodes?workspace_id=" + daemon.DefaultWorkspaceID,
	} {
		recorded := fleet.raw(t, path)
		if bytes.Contains(recorded, []byte(fleet.bootstrapToken)) {
			t.Fatalf("%s carries the invitation the machine was bootstrapped with", path)
		}
	}
}

// raw is one operator read, answered as the bytes an operator receives. A byte
// scan needs the bytes: decoding into a struct would only ever search the fields
// the struct happens to name, and the whole point is material arriving through a
// field nobody named.
func (f *fleet) raw(t *testing.T, path string) []byte {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+f.address+path, http.NoBody)
	if err != nil {
		t.Fatalf("build GET %s: %v", path, err)
	}
	request.Header.Set("Authorization", "Bearer "+f.token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("call GET %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", path, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, response.StatusCode, body)
	}
	return body
}
