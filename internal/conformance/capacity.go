package conformance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/capability/capacitytest"
	"github.com/benngarcia/mercator/internal/providers"
)

// This file is the capacity mode of `mercator verify`: the bounded suite of
// promises every CapacityProvider keeps, run against a live connection.
//
// It launches no Run and stands up no control plane, and that is what makes it
// bounded. What a capacity trial establishes is that a backend keeps the
// capacity contract, which is answered by renting a machine and giving it back;
// whether an agent then enrols on one is the node registry's answer and needs
// the whole daemon, which is the launch modes' job.
//
// It still refuses the callback topology a launch trial refuses. Every machine
// it rents is handed a bootstrap naming the public origin, and a trial that
// wrote an origin nothing serves onto a real machine could not tell a provider
// defect from a control plane nobody could have reached.

// verifyCapacity keeps every promise against one connection and reports each by
// name, then sweeps whatever the promises left behind.
func (runner *Runner) verifyCapacity(ctx context.Context, trial Trial, identity trialIdentity, evidence Evidence) (Evidence, error) {
	provider, failure := runner.capacityProvider(trial)
	if failure != nil {
		evidence.Verdict = VerdictBlocked
		evidence.Failure = failure
		return evidence, nil
	}
	subject, err := runner.subject(trial, identity, provider)
	if err != nil {
		return evidence, err
	}
	evidence.WorkspaceID = subject.Lease.WorkspaceID
	listingQuery := capability.CapacityQuery{WorkspaceID: subject.Lease.WorkspaceID}
	if listing, err := capacitytest.Affordable(ctx, provider, listingQuery, trial.MaxExpectedCostUSD, trial.Timeout); err == nil {
		evidence.Offer = offerEvidence(listing, trial.Timeout)
	}

	evidence.Promises = keepEveryPromise(ctx, subject)
	evidence.Verdict = VerdictPassed
	for _, promise := range evidence.Promises {
		if promise.Outcome == PromiseBroken {
			evidence.Verdict = VerdictFailed
			evidence.Failure = &TrialFailure{Code: "CAPACITY_PROMISE_BROKEN", Message: promise.Name + ": " + promise.Detail}
			break
		}
	}
	return runner.sweep(ctx, subject, evidence), nil
}

// keepEveryPromise runs the shared suite and records what each promise found. A
// broken promise does not stop the rest: an operator reading the evidence wants
// every promise this backend breaks, not the first one.
func keepEveryPromise(ctx context.Context, subject capacitytest.Subject) []PromiseEvidence {
	kept := make([]PromiseEvidence, 0, len(capacitytest.Promises()))
	for _, promise := range capacitytest.Promises() {
		record := PromiseEvidence{Name: promise.Name, Rule: promise.Rule, Outcome: PromiseKept}
		switch err := promise.Keep(ctx, subject); {
		case errors.Is(err, capacitytest.ErrNotApplicable):
			record.Outcome = PromiseOutOfReach
			record.Detail = err.Error()
		case err != nil:
			record.Outcome = PromiseBroken
			record.Detail = err.Error()
		}
		kept = append(kept, record)
	}
	return kept
}

// sweep is the trial's own backstop. Every promise gives its machine back before
// it returns, so this finds nothing on a passing trial; a promise that failed
// between renting and returning is exactly the case where a trial would
// otherwise leave a machine billing.
func (runner *Runner) sweep(ctx context.Context, subject capacitytest.Subject, evidence Evidence) Evidence {
	if !subject.Provider.CapacitySupport().ListOwned {
		return evidence
	}
	sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sweepTimeout)
	defer cancel()
	owned, err := reclaim(sweepCtx, subject)
	evidence.Inventory.Owned = len(owned)
	if err == nil && len(owned) == 0 {
		return evidence
	}
	evidence.Verdict = VerdictFailed
	evidence.CleanupFailure = &TrialFailure{
		Code:    "CLEANUP_FAILED",
		Message: fmt.Sprintf("this trial's connection still holds %d machines: %v", len(owned), errors.Join(err, ownedRefs(owned))),
	}
	return evidence
}

// reclaim destroys everything this trial's workspace still owns and answers with
// what survived the attempt.
//
// Each destruction is keyed by the machine as well as the lease it was taken out
// under. What the sweep finds is exactly the capacity nothing else accounted for,
// which is where two machines wearing one Rental's tag turn up, and a provider
// honouring the key would answer the second one as a repeat of the first and
// leave it billing.
func reclaim(ctx context.Context, subject capacitytest.Subject) ([]capability.OwnedCapacity, error) {
	var failures error
	for attempt := range sweepAttempts {
		owned, err := subject.Provider.ListOwnedCapacity(ctx, ownershipQuery(subject))
		if err != nil {
			return nil, err
		}
		if len(owned) == 0 {
			return nil, nil
		}
		if attempt == sweepAttempts-1 {
			return owned, failures
		}
		for _, machine := range owned {
			_, err := subject.Provider.TerminateCapacity(ctx, capability.CapacityCommand{
				CapacityRef: capability.CapacityRef{
					WorkspaceID:    machine.WorkspaceID,
					ConnectionID:   subject.Lease.ConnectionID,
					RentalID:       machine.RentalID,
					NativeRef:      machine.NativeRef,
					OwnershipToken: machine.OwnershipToken,
				},
				OperationKey: "terminate_" + machine.RentalID + "_" + machine.NativeRef,
				Generation:   machine.Generation,
			})
			failures = errors.Join(failures, err)
		}
	}
	return nil, failures
}

const (
	sweepAttempts = 3
	sweepTimeout  = 2 * time.Minute
)

// capacityProvider builds this trial's connection and asks it for the capacity
// contract. A backend that sells one-shot execution is refused here rather than
// at the first provision, so nothing is allocated to discover the lane, and a
// connection that could not be built at all is a different answer from one built
// in the wrong lane.
func (runner *Runner) capacityProvider(trial Trial) (capability.CapacityProvider, *TrialFailure) {
	factory := runner.providerFactory
	if factory == nil {
		factory = providers.Factory()
	}
	backend, err := factory.Build(trial.AdapterType, trial.Config, runner.config.Environment[trial.CredentialEnv])
	if err != nil {
		return nil, &TrialFailure{Code: "CONNECTION_BUILD_FAILED", Message: err.Error()}
	}
	provider, err := backend.Capacity()
	if err != nil {
		return nil, &TrialFailure{Code: "CONNECTION_SELLS_NO_CAPACITY", Message: err.Error()}
	}
	return provider, nil
}

// subject is the trial's own identity, which every machine it rents is tagged
// with. The workspace is minted here rather than created through the API because
// a capacity trial runs no control plane: what the identity has to do is let the
// sweep find this trial's machines and nobody else's.
func (runner *Runner) subject(trial Trial, identity trialIdentity, provider capability.CapacityProvider) (capacitytest.Subject, error) {
	enrolment, err := randomSecret(32)
	if err != nil {
		return capacitytest.Subject{}, err
	}
	lease := capacitytest.Lease{
		TrialID:      identity.suffix,
		WorkspaceID:  "ws_" + identity.suffix,
		ConnectionID: identity.connectionID,
		// The origin a rented machine is told to report to, which is the one an
		// operator routed to this verifier. Nothing here waits for a machine to
		// arrive, so what it proves is that the trial handed out a reachable
		// address rather than that anything used it.
		ControlPlaneURL: strings.TrimRight(runner.config.PublicURL, "/"),
		AgentVersion:    trialAgentVersion,
		// Material nothing minted. No machine this trial rents is expected to
		// join, and a token that worked would be a credential left on a host for
		// no reason.
		EnrollmentToken: enrolment,
		MaxLifetime:     trial.Timeout,
	}
	return capacitytest.Subject{
		Name:     trial.AdapterType,
		Provider: provider,
		Lease:    lease,
		Capacity: func(ctx context.Context) (capacitytest.Origin, error) {
			listing, err := capacitytest.Affordable(
				ctx,
				provider,
				capability.CapacityQuery{WorkspaceID: lease.WorkspaceID},
				trial.MaxExpectedCostUSD,
				trial.Timeout,
			)
			return capacitytest.OriginOf(listing), err
		},
	}, nil
}

// trialAgentVersion is the node agent build a bootstrap names. A capacity trial
// asks a provider to deliver the bootstrap verbatim and never waits for the
// agent, so the build only has to be a real one for the delivery to be real.
const trialAgentVersion = "v0.7.1"

func ownershipQuery(subject capacitytest.Subject) capability.OwnershipQuery {
	return capability.OwnershipQuery{WorkspaceID: subject.Lease.WorkspaceID}
}

func ownedRefs(owned []capability.OwnedCapacity) error {
	if len(owned) == 0 {
		return nil
	}
	refs := make([]string, 0, len(owned))
	for _, machine := range owned {
		refs = append(refs, machine.RentalID+"="+machine.NativeRef)
	}
	return errors.New(strings.Join(refs, " "))
}
