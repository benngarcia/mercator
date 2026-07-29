package domain

import (
	"fmt"
	"time"
)

// This file is what Mercator hands a machine so it can fetch one piece of
// content, and nothing else.
//
// A node is a host an operator rents by the hour, and everything Mercator gives
// it is material an attacker who takes that host also has. The registry account
// that can read every private image in the workspace, and the object-store key
// that can read every Artifact ever published, are therefore never what a
// machine holds. What it holds is one credential minted for one fetch: it names
// the operation it was minted for, the workspace whose content it reaches, the
// content itself, and the moment it stops being accepted, and the machine
// forgets it when the fetch ends.
//
// The scope is not decoration. It is what the node checks before presenting the
// material, so a credential that arrived for another workspace's pull is refused
// on the machine rather than spent, and it is what a reader of the record can
// hold Mercator to: a credential nobody can name a bound for is the account
// itself under another name.

// ContentCredentialScope is what every credential Mercator mints for a machine
// states about itself. All four are required, and each of them is a way the
// material can be narrower than the account behind it: one operation rather than
// a standing right, one workspace rather than the fleet, one piece of content
// rather than a repository, and a window rather than for ever.
type ContentCredentialScope struct {
	// Operation is the one node command this material was minted for, named the
	// way the control plane and the machine both name it.
	Operation string `json:"operation"`
	// WorkspaceID is whose content this reaches. It is stated rather than
	// inferred from the operation, because the operation is a string two
	// workspaces could conceivably agree on and the tenancy boundary may not
	// rest on that.
	WorkspaceID string `json:"workspace_id"`
	// Content is the one image digest or Artifact version this authorises.
	Content string `json:"content"`
	// ExpiresAt is when the material stops being presentable. A credential
	// without one outlives the operation it was minted for, which is the whole
	// of what this type exists to prevent.
	ExpiresAt time.Time `json:"expires_at"`
}

// Zero reports that this scope states nothing. It is deliberately not what
// "nothing was minted" means: material carrying no scope is the thing this file
// exists to catch, so each credential below answers that question about the
// whole of itself rather than about its bound alone.
func (scope ContentCredentialScope) Zero() bool {
	return scope == ContentCredentialScope{}
}

// Authorises is the check a machine makes before it presents this material. It
// answers with the reason rather than a bare no, because every way it can fail
// is a different incident: material with no bound, material minted for somebody
// else's content, and material whose window has closed are three different
// things for an operator to read in a node's refusal.
func (scope ContentCredentialScope) Authorises(operation, workspaceID, content string, at time.Time) error {
	switch {
	case scope.Operation == "" || scope.WorkspaceID == "" || scope.Content == "" || scope.ExpiresAt.IsZero():
		return fmt.Errorf(
			"this credential names operation %q, workspace %q, content %q and expiry %s, and a credential that cannot state all four is the account it was minted from",
			scope.Operation, scope.WorkspaceID, scope.Content, scope.ExpiresAt,
		)
	case scope.Operation != operation:
		return fmt.Errorf("this credential was minted for operation %q and is being presented for %q", scope.Operation, operation)
	case scope.WorkspaceID != workspaceID:
		return fmt.Errorf("this credential was minted for workspace %q and is being presented for %q", scope.WorkspaceID, workspaceID)
	case scope.Content != content:
		return fmt.Errorf("this credential was minted for %q and is being presented for %q", scope.Content, content)
	case !at.Before(scope.ExpiresAt):
		return fmt.Errorf("this credential expired at %s and it is now %s", scope.ExpiresAt.UTC(), at.UTC())
	default:
		return nil
	}
}

// RegistryPull is one registry read minted for one pull. The username and secret
// are whatever the registry accepts for that read, which against a registry that
// issues its own tokens is the exchanged token and against one that only knows
// how to check a password is an account narrowed to this content. Either way the
// scope above is what the machine holds it to.
type RegistryPull struct {
	ContentCredentialScope
	// Registry is the host this material may be presented to, and nowhere else.
	// A machine that carried it to another host would be handing one registry's
	// operator another registry's credential.
	Registry string `json:"registry"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

// Zero reports that nothing was minted for this pull. It is a real answer rather
// than a missing one: a public image needs no credential, and a machine that
// presents none for it is behaving correctly.
//
// It asks about the material as well as the bound, because a value that answered
// on the bound alone would call a bare username and password "nothing minted"
// and let the one case worth catching past every reader of this type: material
// with no scope is the registry account under another name, and it has to be
// visible to be refused.
func (pull RegistryPull) Zero() bool {
	return pull == RegistryPull{}
}

// ArtifactRead is one object-store read minted as a location. A presigned GET is
// a bearer credential written as a URL, which is why it is modelled here beside
// the registry material rather than as an address: anything holding it can read
// that object, so it expires, it names one object, and it never enters the
// record.
//
// The durable location the catalog states stays where it was. That one is a name
// for content and is safe to write down, and keeping the two apart is what lets
// a Run Bundle say which Artifact a machine fetched without carrying a working
// read of it.
type ArtifactRead struct {
	ContentCredentialScope
	Location string `json:"location"`
}

// Zero reports that nothing was minted for this read, on the same terms as a
// pull: a signed location with no bound beside it is exactly what must not go
// unnoticed, so the location counts.
func (read ArtifactRead) Zero() bool {
	return read == ArtifactRead{}
}
