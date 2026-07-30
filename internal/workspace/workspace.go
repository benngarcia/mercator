package workspace

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const MigrationPrincipal = "system:migration"

var (
	ErrNotFound      = errors.New("workspace: not found")
	ErrArchived      = errors.New("workspace: archived")
	ErrAlreadyExists = errors.New("workspace: already exists")
)

type Workspace struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"display_name"`
	CreatedAt   time.Time  `json:"created_at"`
	CreatedBy   string     `json:"created_by"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
}

type Create struct {
	ID          string
	DisplayName string
	CreatedAt   time.Time
	CreatedBy   string
}

type ListOptions struct {
	IncludeArchived bool
	// Subject narrows the listing to the workspaces one subject is a member of.
	// Empty lists every workspace, which is what an instance credential asking
	// on the deployment's own behalf sees.
	Subject string
}

func (c Create) validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("workspace: workspace_id is required")
	}
	if strings.TrimSpace(c.DisplayName) == "" {
		return fmt.Errorf("workspace: display_name is required")
	}
	if c.CreatedAt.IsZero() {
		return fmt.Errorf("workspace: created_at is required")
	}
	if strings.TrimSpace(c.CreatedBy) == "" {
		return fmt.Errorf("workspace: created_by is required")
	}
	return nil
}
