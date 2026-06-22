package authz

import "fmt"

type Action string

const (
	ActionRead  Action = "read"
	ActionWrite Action = "write"
)

type ResourceType string

const (
	ResourceRun        ResourceType = "run"
	ResourceWorkload   ResourceType = "workload"
	ResourceConnection ResourceType = "connection"
	ResourceSecret     ResourceType = "secret"
)

type Principal struct {
	Subject      string
	WorkspaceIDs []string
}

type Authorizer struct{}

func New() Authorizer {
	return Authorizer{}
}

func (Authorizer) Authorize(principal Principal, action Action, resource ResourceType, workspaceID string) error {
	if principal.Subject == "" {
		return fmt.Errorf("authz: subject is required")
	}
	for _, allowed := range principal.WorkspaceIDs {
		if allowed == workspaceID {
			return nil
		}
	}
	return fmt.Errorf("authz: %s cannot %s %s in workspace %s", principal.Subject, action, resource, workspaceID)
}
