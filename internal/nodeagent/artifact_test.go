package nodeagent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// signedRead is a presigned GET as an object store answers with one: a bearer
// credential written as a URL, whose query carries the account it was signed by
// and the signature itself.
const signedRead = "https://objects.invalid/mercator/ws_1/artifacts/corpus" +
	"?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=lab-object-store%2F20260728&X-Amz-Signature=deadbeefdeadbeef"

// TestAFetchThatCouldNotReachTheStoreNamesTheTransportAndNotTheRead is what a
// node says when the object store is unreachable, and it is deliberately not
// what net/http says. A failed request comes back as a *url.Error whose message
// is the whole request URL, signature and all, and this node's answer becomes
// the operation's failure, which the control plane stores durably in a column
// whose contract is that it never carries credential material. Wrapping the
// standard library's error would put a working read of the object into that
// record, still good for the rest of its window.
func TestAFetchThatCouldNotReachTheStoreNamesTheTransportAndNotTheRead(t *testing.T) {
	runtime := NewDockerRuntime("", WithArtifactRoot(t.TempDir()))
	command := unreachableFetch(t)

	err := runtime.PrepareArtifact(context.Background(), command)

	if err == nil {
		t.Fatal("a fetch from a store that does not resolve reported success")
	}
	for _, material := range []string{"X-Amz-Signature", "X-Amz-Credential", "deadbeefdeadbeef"} {
		if strings.Contains(err.Error(), material) {
			t.Fatalf("the failure a node reports carries %s of the minted read: %v", material, err)
		}
	}
	if !strings.Contains(err.Error(), "artifact:corpus:v3") {
		t.Fatalf("the failure names no version, so an operator cannot say which fetch broke: %v", err)
	}
}

// TestAPullHandedOverAsARecordIsRefusedRatherThanPresented is the machine's side
// of keeping material out of the durable record. A command is written down so an
// agent that was disconnected still receives it, and the credential inside it is
// not, so a replayed prepare arrives stating which pull was authorised and
// carrying nothing to present. The node says that in Mercator's vocabulary
// rather than sending an empty password and reporting back whatever the registry
// said about it.
func TestAPullHandedOverAsARecordIsRefusedRatherThanPresented(t *testing.T) {
	runtime := NewDockerRuntime("")
	command := replayedPull()

	err := runtime.PrepareImage(context.Background(), command)

	if err == nil {
		t.Fatal("a node presented a pull it holds no material for")
	}
	if !strings.Contains(err.Error(), "the record of a pull") {
		t.Fatalf("the refusal reads as a registry failure rather than a missing credential: %v", err)
	}
}

func unreachableFetch(t *testing.T) capability.PrepareArtifactCommand {
	t.Helper()
	command := capability.PrepareArtifactCommand{
		ArtifactID:    "artifact:corpus:v3",
		ContentDigest: "sha256:" + strings.Repeat("cd", 32),
		Source:        "objects://mercator/ws_1/artifacts/corpus",
		SourceCredential: domain.ArtifactRead{
			ContentCredentialScope: domain.ContentCredentialScope{
				Operation:   "prepare:artifact:nod_alpha:artifact:corpus:v3",
				WorkspaceID: "ws_1",
				Content:     "artifact:corpus:v3",
				ExpiresAt:   time.Now().UTC().Add(15 * time.Minute),
			},
			Location: signedRead,
		},
	}
	command.WorkspaceID = "ws_1"
	command.OperationID = "prepare:artifact:nod_alpha:artifact:corpus:v3"
	return command
}

func replayedPull() capability.PrepareImageCommand {
	digest := "sha256:" + strings.Repeat("ab", 32)
	command := capability.PrepareImageCommand{
		ManifestDigest: digest,
		Reference:      "registry.test/analyst@" + digest,
		RegistryCredential: domain.RegistryPull{
			ContentCredentialScope: domain.ContentCredentialScope{
				Operation:   "prepare:image:nod_alpha:" + digest,
				WorkspaceID: "ws_1",
				Content:     digest,
				ExpiresAt:   time.Now().UTC().Add(15 * time.Minute),
			},
			Registry: "registry.test",
			Username: "mercator",
		},
	}
	command.WorkspaceID = "ws_1"
	command.OperationID = "prepare:image:nod_alpha:" + digest
	return command
}
