package lab

import (
	"fmt"
	"time"
)

// contentCredentialsAreScopedAndExpiring is the law on everything Mercator hands
// a machine so it can fetch content. A node is a host an operator rents by the
// hour, and every credential on it is material an attacker who takes that host
// also has, so what crosses to it is never the registry account that can read the
// workspace's private images or the object-store key that can read every Artifact
// ever published. What crosses is one fetch, and four things hold of it.
//
// It states a bound. A credential that cannot name the operation, the workspace,
// the content and the moment it stops being accepted is the account it was minted
// from under another name: nothing about it is narrower than the thing behind it,
// and no reader of the record can hold Mercator to anything.
//
// It states this fetch's bound. A credential is checked against the command it
// arrived on rather than against itself, because a credential minted for another
// workspace's pull is perfectly consistent internally and is exactly the material
// that must never be spent here.
//
// It has not already lapsed. An expiry behind the moment the machine was handed
// the credential is an expiry nothing enforces, which is the same as none.
//
// It does not outlive the fetch. An expiry ahead of the moment it was handed
// over is not enough on its own: a read good for a day is still ahead of every
// moment in the execution, and is a standing right an attacker who takes the host
// tomorrow can still spend. So the window is bounded as well as ahead, which is
// the clause the one above cannot reach.
func contentCredentialsAreScopedAndExpiring(observation InvariantObservation) error {
	for _, credential := range observation.ContentCredentials {
		if err := credential.statesABound(); err != nil {
			return err
		}
		if err := credential.matchesTheFetchItArrivedOn(); err != nil {
			return err
		}
		if err := credential.stillAhead(); err != nil {
			return err
		}
		if err := credential.insideTheWindow(); err != nil {
			return err
		}
	}
	return nil
}

// contentCredentialWindow is the longest a credential handed to a machine may
// stay presentable. It is the Lab's own bound rather than production's constant,
// because a rule that read the number production mints with would agree with
// production by construction and could never fail.
//
// An hour is far longer than any fetch any Blueprint here describes and far
// shorter than anything an operator would call a standing right, so a control
// plane minting a working read of the object store for a day is caught while a
// generous window for a large image over a slow link is not.
const contentCredentialWindow = time.Hour

func (credential contentCredential) statesABound() error {
	switch {
	case credential.Scope.Operation == "":
		return credential.violation("names no operation, so nothing about it is narrower than the account behind it")
	case credential.Scope.WorkspaceID == "":
		return credential.violation("names no workspace, so nothing stops it reaching another tenant's content")
	case credential.Scope.Content == "":
		return credential.violation("names no content, so it reads whatever the account behind it can read")
	case credential.Scope.ExpiresAt.IsZero():
		return credential.violation("states no expiry, so it outlives the fetch it was minted for by the whole life of the machine")
	}
	return nil
}

func (credential contentCredential) matchesTheFetchItArrivedOn() error {
	switch {
	case credential.Scope.Operation != credential.Operation:
		return credential.violation(fmt.Sprintf("was minted for operation %q", credential.Scope.Operation))
	case credential.Scope.WorkspaceID != credential.WorkspaceID:
		return credential.violation(fmt.Sprintf("was minted for workspace %q", credential.Scope.WorkspaceID))
	case credential.Scope.Content != credential.Content:
		return credential.violation(fmt.Sprintf("was minted for content %q", credential.Scope.Content))
	}
	return nil
}

func (credential contentCredential) stillAhead() error {
	if !credential.At.Before(credential.Scope.ExpiresAt) {
		return credential.violation(fmt.Sprintf(
			"expired at %s and was handed over at %s",
			credential.Scope.ExpiresAt.UTC(), credential.At.UTC(),
		))
	}
	return nil
}

func (credential contentCredential) insideTheWindow() error {
	if window := credential.Scope.ExpiresAt.Sub(credential.At); window > contentCredentialWindow {
		return credential.violation(fmt.Sprintf(
			"stays presentable for %s, and anything past %s is a standing right rather than one fetch",
			window, contentCredentialWindow,
		))
	}
	return nil
}

func (credential contentCredential) violation(because string) error {
	return fmt.Errorf(
		"the %s credential handed to a machine for %q in workspace %q %s",
		credential.Kind, credential.Content, credential.WorkspaceID, because,
	)
}
