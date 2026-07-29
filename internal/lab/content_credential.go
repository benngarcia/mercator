package lab

import (
	"fmt"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

// This file is the half of the world that decides whether a machine may have the
// content it was asked to fetch. A registry that serves a private image to
// anyone who asks and an object store that hands out its bytes to an
// unauthenticated GET are a world in which a control plane that mints nothing
// looks exactly like one that mints correctly, so nothing here would ever be
// caught. What is modelled instead is the two refusals production really meets:
// a registry that answers an anonymous read with a denial, and an object store
// that only serves a signed one.

// contentCredential is one credential this world watched Mercator hand a
// machine, beside what the command it arrived on was really for. The two halves
// are separate on purpose. What the credential says about itself is a claim, and
// what the command is about is the fact; a rule that read only the claim would
// pass a credential minted for another workspace as long as it was internally
// consistent.
type contentCredential struct {
	// Kind is which of the two fetches this was, so a violation names the act
	// rather than a bare string.
	Kind adapter.PrepareKind
	// Operation, WorkspaceID and Content are what the machine was told to do,
	// read off the command.
	Operation   string
	WorkspaceID string
	Content     string
	// Scope is what the credential states about itself.
	Scope domain.ContentCredentialScope
	// Material is what the machine would present: the registry secret, or the
	// signed location a presigned read is written as. It is here so a rule can
	// ask whether one fetch's material turned up on another's, and it is exported
	// by nothing, exactly as an enrollment token is not.
	Material string
	// At is when the machine was handed it, which is what its expiry is read
	// against.
	At time.Time
}

// noteContentCredentials records what one preparation item carried. Material a
// machine was never given is not recorded: a public image is minted nothing, and
// filing a blank credential against it would have every rule about scope fail on
// content that correctly needed none.
func (world *simulatedWorld) noteContentCredentials(workspaceID string, item adapter.PrepareItem) {
	if pull := item.RegistryCredential; !pull.Zero() {
		world.handedOver = append(world.handedOver, world.handed(workspaceID, item, pull.ContentCredentialScope, pull.Secret))
	}
	if read := item.SourceCredential; !read.Zero() {
		world.handedOver = append(world.handedOver, world.handed(workspaceID, item, read.ContentCredentialScope, read.Location))
	}
}

func (world *simulatedWorld) handed(
	workspaceID string,
	item adapter.PrepareItem,
	scope domain.ContentCredentialScope,
	material string,
) contentCredential {
	return contentCredential{
		Kind:        item.Kind,
		Operation:   item.Operation(),
		WorkspaceID: workspaceID,
		Content:     item.Content(),
		Scope:       scope,
		Material:    material,
		At:          world.now,
	}
}

// contentRefusal is a fetch being turned away, and the reason. An empty answer
// is content this machine may have.
//
// Two different parties can turn one fetch away and this keeps them apart,
// because production does. The machine checks the material it was handed against
// the command it arrived on before presenting it, and that check is Mercator's
// own code running on the node. The far side then checks whatever it can check,
// which is a different and usually smaller thing. Collapsing the two would let
// the Lab describe a registry that enforces Mercator's scope, and no registry
// does.
func (world *simulatedWorld) contentRefusal(workspaceID string, item adapter.PrepareItem) string {
	if reason := world.machineRefusal(workspaceID, item); reason != "" {
		return reason
	}
	if item.Kind == adapter.PrepareImage {
		return world.registryRefusal(item)
	}
	return world.objectStoreRefusal(item)
}

// machineRefusal is the node holding its own material to the command it arrived
// on, which is what DockerRuntime.authorisedPull and authorisedRead do on a real
// host. It is the whole of what enforces a scope a password registry cannot see,
// so a Lab that left it out would be describing a bound nothing anywhere checks.
//
// No credential is not a refusal here. Content any anonymous reader can have is
// minted nothing, and whether the far side serves it is the far side's answer
// below.
func (world *simulatedWorld) machineRefusal(workspaceID string, item adapter.PrepareItem) string {
	if item.RegistryCredential.Zero() && item.SourceCredential.Zero() {
		return ""
	}
	scope := item.SourceCredential.ContentCredentialScope
	if item.Kind == adapter.PrepareImage {
		scope = item.RegistryCredential.ContentCredentialScope
	}
	if err := scope.Authorises(item.Operation(), workspaceID, item.Content(), world.now); err != nil {
		return err.Error()
	}
	return ""
}

// registryRefusal is what the registry itself says, which is far less than the
// scope. A password registry checks the password and has never heard of an
// operation, a workspace, a digest or an expiry, so what it can turn away is a
// reader presenting nothing and a reader presenting another host's account. An
// image anyone can read is served to anyone.
func (world *simulatedWorld) registryRefusal(item adapter.PrepareItem) string {
	if !world.images[item.Image].Private {
		return ""
	}
	pull := item.RegistryCredential
	if pull.Secret == "" {
		return fmt.Sprintf("the registry serving %s refuses an anonymous read", item.Image)
	}
	if host := domain.ReferenceRegistry(item.Image); pull.Registry != host {
		return fmt.Sprintf("this credential is %s's and %s is served from %s", pull.Registry, item.Image, host)
	}
	return ""
}

// objectStoreRefusal is what the durable authority says, and it is the one far
// side here that really does enforce the bound: a presigned read is a signature
// over one object and one window, so the store rejects a read of anything else
// and a read whose window has closed without being told anything by Mercator.
// A fetch arriving with nothing is refused rather than served, because the
// durable location the catalog states is a name for content and never a way in.
func (world *simulatedWorld) objectStoreRefusal(item adapter.PrepareItem) string {
	read := item.SourceCredential
	if read.Location == "" {
		return fmt.Sprintf("the object store serves %s only to a minted read, and this fetch presented none", item.ArtifactID)
	}
	if !world.now.Before(read.ExpiresAt) {
		return fmt.Sprintf(
			"the object store refuses a read of %s signed to expire at %s, and it is now %s",
			item.ArtifactID, read.ExpiresAt.UTC(), world.now.UTC(),
		)
	}
	if read.Content != item.Content() {
		return fmt.Sprintf("the object store refuses a read signed for %s presented for %s", read.Content, item.Content())
	}
	return ""
}
