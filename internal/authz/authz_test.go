package authz

import (
	"testing"
)

func TestAuthorizerAllowsWorkspaceScopedOperations(t *testing.T) {
	authorizer := New()
	principal := Principal{Subject: "user_1", WorkspaceIDs: []string{"ws_1"}}
	for _, resource := range []ResourceType{ResourceRun, ResourceWorkload, ResourceConnection, ResourceSecret} {
		if err := authorizer.Authorize(principal, ActionRead, resource, "ws_1"); err != nil {
			t.Fatalf("expected allow for %s: %v", resource, err)
		}
	}
}

func TestAuthorizerRejectsCrossWorkspaceOperations(t *testing.T) {
	authorizer := New()
	principal := Principal{Subject: "user_1", WorkspaceIDs: []string{"ws_1"}}
	if err := authorizer.Authorize(principal, ActionWrite, ResourceRun, "ws_2"); err == nil {
		t.Fatal("expected cross-workspace operation to be rejected")
	}
}
