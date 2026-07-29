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

// contentRefusal is the registry or the object store turning a fetch away, and
// the reason it gave. An empty answer is content this machine may have.
//
// It is the world's own decision rather than a check Mercator makes, which is
// the point: production's refusal comes from the far side, and a Lab that
// enforced this in the control plane would prove the control plane agrees with
// itself.
func (world *simulatedWorld) contentRefusal(workspaceID string, item adapter.PrepareItem) string {
	if item.Kind == adapter.PrepareImage {
		return world.registryRefusal(workspaceID, item)
	}
	return world.objectStoreRefusal(workspaceID, item)
}

// registryRefusal is what the registry says. An image anyone can read is served
// to anyone, so a machine presenting nothing for one is behaving correctly and a
// world that refused it would be describing no registry that exists.
func (world *simulatedWorld) registryRefusal(workspaceID string, item adapter.PrepareItem) string {
	if !world.images[item.Image].Private {
		return ""
	}
	pull := item.RegistryCredential
	if pull.Zero() {
		return fmt.Sprintf("the registry serving %s refuses an anonymous read", item.Image)
	}
	if err := pull.Authorises(item.Operation(), workspaceID, item.Content(), world.now); err != nil {
		return err.Error()
	}
	if host := domain.ReferenceRegistry(item.Image); pull.Registry != host {
		return fmt.Sprintf("this credential is %s's and %s is served from %s", pull.Registry, item.Image, host)
	}
	return ""
}

// objectStoreRefusal is what the durable authority says. Every read of it is
// signed, so a fetch arriving with nothing is refused rather than served: the
// durable location the catalog states is a name for content and never a way in.
func (world *simulatedWorld) objectStoreRefusal(workspaceID string, item adapter.PrepareItem) string {
	read := item.SourceCredential
	if read.Zero() {
		return fmt.Sprintf("the object store serves %s only to a minted read, and this fetch presented none", item.ArtifactID)
	}
	if err := read.Authorises(item.Operation(), workspaceID, item.Content(), world.now); err != nil {
		return err.Error()
	}
	return ""
}
