package workspace

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotMember is what the catalog answers when a subject holds no standing in
// a workspace. It is separate from ErrNotFound on purpose: whether a stranger
// is allowed to tell "there is no such workspace" from "there is one and it is
// not yours" is the caller's decision, not the store's.
var ErrNotMember = errors.New("workspace: not a member")

// Role is what a member may do with the workspace they belong to. An admin is
// whoever brought the workspace into existence, or whoever they hand that
// standing to; a member works inside it.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

func (r Role) Known() bool {
	return r == RoleAdmin || r == RoleMember
}

// Membership is one subject's standing in one workspace. The pair
// (WorkspaceID, Subject) identifies it: a subject holds exactly one role in a
// workspace, and holding none is what makes them a stranger to it.
//
// Subject is the same string the API records as the acting principal: a
// human's email, or the name of a machine identity.
type Membership struct {
	WorkspaceID string    `json:"workspace_id"`
	Subject     string    `json:"subject"`
	Role        Role      `json:"role"`
	GrantedAt   time.Time `json:"granted_at"`
}

func (m Membership) validate() error {
	if strings.TrimSpace(m.WorkspaceID) == "" {
		return fmt.Errorf("workspace: membership workspace_id is required")
	}
	if strings.TrimSpace(m.Subject) == "" {
		return fmt.Errorf("workspace: membership subject is required")
	}
	if !m.Role.Known() {
		return fmt.Errorf("workspace: %q is not a role", m.Role)
	}
	if m.GrantedAt.IsZero() {
		return fmt.Errorf("workspace: membership granted_at is required")
	}
	return nil
}
