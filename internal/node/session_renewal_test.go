package node_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/node"
)

// TestAMachineKeepsWorkingPastItsFirstSession is the case nothing in this tree
// could fail before, because every case finished inside the thirty minutes a
// session credential is good for.
//
// A machine that enrolled half an hour ago is still running the work it was
// given. Its credential has lapsed, and the only other material it ever held is
// the invitation it joined with, which this registry spent the moment it was
// redeemed. So it renews, and the credential it is handed authenticates it for
// everything a session authenticates: the command stream it reads work from, the
// events it owes, and the results it reports.
func TestAMachineKeepsWorkingPastItsFirstSession(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)

	clock.Advance(node.DefaultSession - time.Minute)
	renewal, err := registry.RenewSession(context.Background(), bootstrap.NodeID, enrollment.SessionToken)
	if err != nil {
		t.Fatalf("renew the session of a machine that is still working: %v", err)
	}
	clock.Advance(2 * time.Minute)

	if renewal.SessionToken == enrollment.SessionToken {
		t.Fatal("a renewal handed back the credential it was renewing, which lapses at the same moment")
	}
	if renewal.FencingToken != enrollment.FencingToken {
		t.Fatalf("renewing moved the fencing token from %d to %d, which supersedes a machine that did nothing",
			enrollment.FencingToken, renewal.FencingToken)
	}
	if !renewal.SessionExpires.After(clock.Now()) {
		t.Fatalf("the renewed credential expires at %s, which is not after now %s", renewal.SessionExpires, clock.Now())
	}
	session := openSession(t, registry, bootstrap.NodeID, renewal.SessionToken)
	if session.FencingToken != enrollment.FencingToken {
		t.Fatalf("the renewed session opened at fencing token %d, want %d", session.FencingToken, enrollment.FencingToken)
	}
	if _, err := registry.LaunchWorkload(context.Background(), launchCommand(bootstrap, enrollment, "op-launch-past-the-session")); err != nil {
		t.Fatalf("a machine that renewed its session was not asked to do anything: %v", err)
	}
	receiveCommand(t, session)
}

// TestTheCredentialAMachineLapsedWithRenewsNothing is the other half. Renewal is
// authenticated by a credential that is still good, which is what makes renewing
// early the agent's job rather than the registry's leniency. A machine that let
// its session lapse has nothing left that opens any door here, and the way back
// is a fresh invitation.
func TestTheCredentialAMachineLapsedWithRenewsNothing(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)

	clock.Advance(node.DefaultSession + time.Minute)
	_, err := registry.RenewSession(context.Background(), bootstrap.NodeID, enrollment.SessionToken)

	if err == nil {
		t.Fatal("a lapsed credential renewed itself, so a session credential never really expires")
	}
}

// TestAMachineWhoseGenerationEndedRenewsNothing keeps renewal on the side of the
// line every other door in this registry is on: asking for something is refused a
// retired machine, and reporting what already happened is kept. Retirement is
// also the whole of what bounds a leaked credential, because a bearer token is
// good for whoever holds it, and a renewal that outlived the generation would
// take that answer away.
func TestAMachineWhoseGenerationEndedRenewsNothing(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	if err := registry.Retire(context.Background(), bootstrap.NodeID); err != nil {
		t.Fatalf("retire the node: %v", err)
	}

	_, err := registry.RenewSession(context.Background(), bootstrap.NodeID, enrollment.SessionToken)

	if !errors.Is(err, node.ErrRetired) {
		t.Fatalf("renewal on a Rental generation that is over = %v, want ErrRetired", err)
	}
}

// TestAReplayedInvitationIsRefusedByBothDoorsAndSaysWhich pins the two guards
// enrollment really has, because they refuse a replay at different moments and
// for different reasons. Inside the invitation window the signature is still
// perfectly good and the store's spend record is the only thing that objects.
// Past the window the signer objects on its own, which holds even against a store
// that lost the record.
//
// The answers stay apart because the remedies do. A machine refused as spent
// needs a fresh invitation; a machine refused as invalid may be presenting
// material minted for something else entirely.
func TestAReplayedInvitationIsRefusedByBothDoorsAndSaysWhich(t *testing.T) {
	for name, replay := range map[string]struct {
		after time.Duration
		want  error
	}{
		"inside the window, where the signature is still good": {
			after: time.Minute,
			want:  node.ErrEnrollmentSpent,
		},
		"past the window, where the signer refuses it alone": {
			after: node.DefaultInvitation + time.Minute,
			want:  node.ErrEnrollmentInvalid,
		},
	} {
		t.Run(name, func(t *testing.T) {
			registry, clock := newRegistry(t)
			bootstrap := invite(t, registry)
			enroll(t, registry, bootstrap)

			clock.Advance(replay.after)
			_, err := registry.Enroll(context.Background(), enrollmentRequest(bootstrap))

			if !errors.Is(err, replay.want) {
				t.Fatalf("replaying a redeemed invitation = %v, want %v", err, replay.want)
			}
		})
	}
}
