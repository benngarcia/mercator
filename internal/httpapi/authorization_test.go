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
	"github.com/benngarcia/mercator/internal/node"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scheduler"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/webauth"
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

func TestAStrangerCannotCreateAWorkloadInAnothersWorkspace(t *testing.T) {
	// Arrange
	handler, _ := newTenantHandler(t)
	workspaceID := createWorkspaceAs(t, handler, ana, "Ana's workspace")
	body := `{"workspace_id":"` + workspaceID + `","workload_id":"wrk_squat","name":"squatted"}`

	// Act
	refused := createWorkloadAs(t, handler, brij, body, "idem_probe_1")

	// Assert
	if refused.Code != http.StatusForbidden {
		t.Fatalf("a stranger creating a workload = %d, want 403: %s", refused.Code, refused.Body)
	}
	// The stream is opened at version 0, so a create that landed would have
	// taken the id. Ana taking it afterwards is what proves none did.
	byTheMember := createWorkloadAs(t, handler, ana, body, "idem_probe_2")
	if byTheMember.Code != http.StatusAccepted {
		t.Fatalf("the member creating the same workload = %d, want 202: %s", byTheMember.Code, byTheMember.Body)
	}
}

func TestAStrangerCannotListAnothersNodes(t *testing.T) {
	// Arrange
	registry := node.NewRegistry(node.NewMemoryStore(), node.NewSigner(node.DeriveKey([]byte("test-master-key"))), "https://mercator.example.com")
	handler, _ := newTenantHandler(t, WithNodes(registry))
	workspaceID := createWorkspaceAs(t, handler, ana, "Ana's workspace")
	inviteNode(t, registry, workspaceID, "nod_secret")

	// Act
	refused := requestAs(t, handler, brij, http.MethodGet, "/v1/nodes?workspace_id="+workspaceID, "")

	// Assert
	if refused.Code == http.StatusOK {
		t.Fatalf("a stranger listing another tenant's nodes = 200: %s", refused.Body)
	}
	if bytes.Contains(refused.Body.Bytes(), []byte("nod_secret")) {
		t.Fatalf("the refusal handed the stranger the inventory: %s", refused.Body)
	}
	listed := requestAs(t, handler, ana, http.MethodGet, "/v1/nodes?workspace_id="+workspaceID, "")
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte("nod_secret")) {
		t.Fatalf("the member listing their own nodes = %d: %s", listed.Code, listed.Body)
	}
}

func TestAStrangerCannotInviteANodeIntoAnothersWorkspace(t *testing.T) {
	// Arrange
	registry := node.NewRegistry(node.NewMemoryStore(), node.NewSigner(node.DeriveKey([]byte("test-master-key"))), "https://mercator.example.com")
	handler, _ := newTenantHandler(t, WithNodes(registry))
	workspaceID := createWorkspaceAs(t, handler, ana, "Ana's workspace")
	invitation := `{"workspace_id":"` + workspaceID + `","node_id":"nod_intruder","rental_id":"rnt_intruder","shadow_price_usd_per_hour":1.5}`

	// Act
	refused := requestAs(t, handler, brij, http.MethodPost, "/v1/nodes", invitation)

	// Assert
	if refused.Code == http.StatusCreated {
		t.Fatalf("a stranger inviting a node into another tenant's workspace = 201: %s", refused.Body)
	}
	if bytes.Contains(refused.Body.Bytes(), []byte("enrollment_token")) {
		t.Fatalf("the refusal handed the stranger enrollment material: %s", refused.Body)
	}
	records, err := registry.List(t.Context(), workspaceID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("the refused invitation left %d node(s) in the workspace: %+v", len(records), records)
	}
}

// TestTheConsoleSessionReachesTheWorkspaceTheInstanceTokenCreated is the
// browser acceptance flow with the browser taken out of it. `mercator serve
// --dev` and `mercator lab serve` both create their first workspace with the
// instance bearer token and then hand a human a local session, so the human is
// no member of anything. Every console read is a 403 unless the deployment says
// that its one human is its operator.
//
// It is here rather than only in the Playwright flow because that flow runs in
// CI alone: this workstation has no Chromium (mercator#197). The break it
// catches reached CI once, as GET /v1/runs and GET /v1/console/events both
// answering 403 to a console that had just signed in.
func TestTheConsoleSessionReachesTheWorkspaceTheInstanceTokenCreated(t *testing.T) {
	// Arrange: the real local authenticator, wired the way `serve --dev` wires
	// it, and a workspace created by the deployment rather than by the human.
	const developer = "developer@localhost"
	localAuth, err := webauth.NewLocal(developer)
	if err != nil {
		t.Fatalf("build local authentication: %v", err)
	}
	handler, _ := newTenantHandler(t, WithWebAuth(localAuth))
	workspaceID := createWorkspaceAsTheInstance(t, handler, "The deployment's own workspace")
	session := establishLocalSession(t, handler)

	// Act
	request := httptest.NewRequest(http.MethodGet, "/v1/runs?workspace_id="+workspaceID, nil)
	request.AddCookie(session)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	// Assert
	if recorder.Code != http.StatusOK {
		t.Fatalf("the local developer reading their own console = %d, want 200: %s", recorder.Code, recorder.Body)
	}
}

// createWorkspaceAsTheInstance creates a workspace with the deployment's own
// bearer token, which is what leaves it with no human member.
func createWorkspaceAsTheInstance(t *testing.T, handler http.Handler, displayName string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/workspaces", bytes.NewBufferString(`{"display_name":"`+displayName+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer secret-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create workspace as the instance = %d: %s", recorder.Code, recorder.Body)
	}
	var response struct {
		Workspace workspace.Workspace `json:"workspace"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode created workspace: %v", err)
	}
	return response.Workspace.ID
}

// establishLocalSession signs in the way the console does, by asking the
// authenticator's own session endpoint for one.
func establishLocalSession(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth/session", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("establish local session = %d: %s", recorder.Code, recorder.Body)
	}
	cookies := (&http.Response{Header: recorder.Header()}).Cookies()
	if len(cookies) == 0 {
		t.Fatal("the local session endpoint set no cookie")
	}
	return cookies[0]
}

func createWorkloadAs(t *testing.T, handler http.Handler, subject, body, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/workloads", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer cli:"+subject)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func inviteNode(t *testing.T, registry *node.Registry, workspaceID, nodeID string) {
	t.Helper()
	if _, err := registry.Invite(t.Context(), node.Invitation{
		WorkspaceID:           workspaceID,
		NodeID:                nodeID,
		RentalID:              "rnt_" + nodeID,
		Generation:            1,
		ShadowPriceUSDPerHour: 1.5,
	}); err != nil {
		t.Fatalf("invite node: %v", err)
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

func TestTheSoleOperatorOfADevelopmentServerIsNotScopedToAWorkspace(t *testing.T) {
	// Arrange: `serve --dev` authenticates exactly one human, who is the
	// deployment's own operator and already holds its bearer token. The
	// authenticator is what says so.
	handler, _ := newTenantHandler(t, WithWebAuth(stubWebAuth{sole: brij}))
	workspaceID := createWorkspaceAs(t, handler, ana, "Ana's workspace")

	// Act
	byTheSoleOperator := getRunsAs(t, handler, brij, workspaceID)

	// Assert
	if byTheSoleOperator.Code != http.StatusOK {
		t.Fatalf("the sole operator reading a workspace = %d, want 200: %s", byTheSoleOperator.Code, byTheSoleOperator.Body)
	}
	// Nobody else is unscoped by it, including on the same server.
	stranger := getRunsAs(t, handler, "cleo@example.com", workspaceID)
	if stranger.Code != http.StatusForbidden {
		t.Fatalf("another human on a sole-operator server = %d, want 403: %s", stranger.Code, stranger.Body)
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
