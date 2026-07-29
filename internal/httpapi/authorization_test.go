package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scheduler"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/workload"
	"github.com/benngarcia/mercator/internal/workspace"
)

// A workspace is the tenancy boundary. These cases put two humans in front of
// the real handler, over the real catalog, and ask what the second one can
// reach of the first one's workspace.
const (
	ana  = "ana@example.com"
	brij = "brij@example.com"
)

func TestAWorkspaceAnswersOnlyToItsMembers(t *testing.T) {
	// Arrange
	handler, _ := newTenantHandler(t)
	workspaceID := createWorkspaceAs(t, handler, ana, "Ana's workspace")

	// Act
	byTheCreator := getRunsAs(t, handler, ana, workspaceID)
	byAStranger := getRunsAs(t, handler, brij, workspaceID)

	// Assert
	if byTheCreator.Code != http.StatusOK {
		t.Fatalf("the creator reading their own workspace = %d, want 200: %s", byTheCreator.Code, byTheCreator.Body)
	}
	if byAStranger.Code != http.StatusForbidden {
		t.Fatalf("a stranger reading someone else's workspace = %d, want 403: %s", byAStranger.Code, byAStranger.Body)
	}
	if bytes.Contains(byAStranger.Body.Bytes(), []byte(workspaceID)) {
		t.Fatalf("the refusal named the workspace back at the stranger: %s", byAStranger.Body)
	}
}

func TestListingWorkspacesShowsOnlyTheSubjectsOwn(t *testing.T) {
	// Arrange
	handler, _ := newTenantHandler(t)
	anasWorkspace := createWorkspaceAs(t, handler, ana, "Ana's workspace")
	brijsWorkspace := createWorkspaceAs(t, handler, brij, "Brij's workspace")

	// Act
	listed := listWorkspacesAs(t, handler, brij)

	// Assert
	if len(listed) != 1 || listed[0].ID != brijsWorkspace {
		t.Fatalf("Brij's workspaces = %+v, want only %s", listed, brijsWorkspace)
	}
	for _, item := range listed {
		if item.ID == anasWorkspace {
			t.Fatalf("Brij was shown Ana's workspace %s", anasWorkspace)
		}
	}
}

func TestArchivingAWorkspaceTheSubjectCannotSeeAnswersNotFound(t *testing.T) {
	// Arrange
	handler, _ := newTenantHandler(t)
	workspaceID := createWorkspaceAs(t, handler, ana, "Ana's workspace")

	// Act
	refused := requestAs(t, handler, brij, http.MethodPost, "/v1/workspaces/"+workspaceID+"/archive", "")

	// Assert
	if refused.Code != http.StatusNotFound {
		t.Fatalf("a stranger archiving = %d, want 404: %s", refused.Code, refused.Body)
	}
	stillActive := listWorkspacesAs(t, handler, ana)
	if len(stillActive) != 1 || stillActive[0].ArchivedAt != nil {
		t.Fatalf("the refused archive changed the workspace: %+v", stillActive)
	}
}

func TestAGrantedMemberReachesTheWorkspaceTheyWereGranted(t *testing.T) {
	// Arrange
	handler, catalog := newTenantHandler(t)
	workspaceID := createWorkspaceAs(t, handler, ana, "Ana's workspace")
	if getRunsAs(t, handler, brij, workspaceID).Code != http.StatusForbidden {
		t.Fatal("Brij should start as a stranger to Ana's workspace")
	}

	// Act
	if err := catalog.Grant(t.Context(), workspace.Membership{
		WorkspaceID: workspaceID,
		Subject:     brij,
		Role:        workspace.RoleMember,
		GrantedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("grant membership: %v", err)
	}

	// Assert
	admitted := getRunsAs(t, handler, brij, workspaceID)
	if admitted.Code != http.StatusOK {
		t.Fatalf("a granted member = %d, want 200: %s", admitted.Code, admitted.Body)
	}
	membership, err := catalog.MembershipOf(t.Context(), workspaceID, brij)
	if err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if membership.Role != workspace.RoleMember {
		t.Fatalf("role = %q, want %q", membership.Role, workspace.RoleMember)
	}
}

func TestTheInstanceCredentialIsNotScopedToAWorkspace(t *testing.T) {
	// Arrange
	handler, _ := newTenantHandler(t)
	workspaceID := createWorkspaceAs(t, handler, ana, "Ana's workspace")

	// Act
	request := httptest.NewRequest(http.MethodGet, "/v1/runs?workspace_id="+workspaceID, nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	// Assert
	if recorder.Code != http.StatusOK {
		t.Fatalf("the instance credential = %d, want 200: %s", recorder.Code, recorder.Body)
	}
}

// newTenantHandler builds the production handler over a real SQLite catalog, so
// a refusal is decided by the store production decides it with rather than by a
// stand-in. Only the identity provider is stubbed: a caller presents
// "cli:<email>" and is that email.
func newTenantHandler(t *testing.T, extra ...Option) (http.Handler, *workspace.SQLiteCatalog) {
	t.Helper()
	storage, err := sqlitestore.Open(t.Context(), "file:"+filepath.Join(t.TempDir(), "mercator.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if err := storage.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	})
	log := storage.EventLog()
	provider := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{httpOffer("off_1", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	options := append([]Option{WithBearerAuth("secret-token"), WithWebAuth(stubWebAuth{})}, extra...)
	handler := New(Deps{
		Orchestrator: orchestrator.New(log, scheduler.New(), provider),
		Offers:       singleProviderOffers{provider: provider},
		Workloads:    workload.New(log),
		Resolver:     ociresolver.NewStaticResolver(nil, ociresolver.WithSyntheticDigests()),
		Workspaces:   storage.Workspaces(),
		Events:       log,
	}, options...)
	return handler, storage.Workspaces()
}

func createWorkspaceAs(t *testing.T, handler http.Handler, subject, displayName string) string {
	t.Helper()
	created := requestAs(t, handler, subject, http.MethodPost, "/v1/workspaces", `{"display_name":"`+displayName+`"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create workspace as %s = %d: %s", subject, created.Code, created.Body)
	}
	var response struct {
		Workspace workspace.Workspace `json:"workspace"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode created workspace: %v", err)
	}
	if response.Workspace.CreatedBy != subject {
		t.Fatalf("created_by = %q, want %q", response.Workspace.CreatedBy, subject)
	}
	return response.Workspace.ID
}

func listWorkspacesAs(t *testing.T, handler http.Handler, subject string) []workspace.Workspace {
	t.Helper()
	listed := requestAs(t, handler, subject, http.MethodGet, "/v1/workspaces", "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list workspaces as %s = %d: %s", subject, listed.Code, listed.Body)
	}
	var response struct {
		Workspaces []workspace.Workspace `json:"workspaces"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode workspace list: %v", err)
	}
	return response.Workspaces
}

func getRunsAs(t *testing.T, handler http.Handler, subject, workspaceID string) *httptest.ResponseRecorder {
	t.Helper()
	return requestAs(t, handler, subject, http.MethodGet, "/v1/runs?workspace_id="+workspaceID, "")
}

func requestAs(t *testing.T, handler http.Handler, subject, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer cli:"+subject)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
