package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/workload"
)

// migrateStoredRevisionSecrets moves a stored workload revision out of the public
// payload it was written into.
//
// compute.workload.revision_created.v1 carried the whole revision in data_json
// and nothing in private_data, so every environment value a caller stored,
// including the tokens callers put there, was readable by every reader of the
// public log and streamed to every console reader of the workspace. The run door
// has redacted exactly these values for as long as it has had a private payload.
// Rewriting the door leaves that history behind, and history is what a console
// reader reads.
//
// The rewrite is the same redaction the door now applies, over the revision each
// event already holds, so the two cannot drift: the whole revision becomes the
// private payload and the public one states each value's kind. It runs after the
// service class rename, which rewrites data_json at this same site, so the
// revision this reads is already in the current vocabulary.
//
// An event that already has a private payload is one this Mercator wrote, and it
// is left alone. A payload that will not decode fails the migration rather than
// being guessed at: this rewrites the record of what a caller asked to run.
func migrateStoredRevisionSecrets(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite storage: begin stored revision migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	public, err := publicRevisionPayloads(ctx, tx)
	if err != nil {
		return err
	}
	for eventID, payload := range public {
		redacted, private, err := splitStoredRevision(eventID, payload)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE events SET data_json = ?, private_data = ? WHERE event_id = ?`,
			redacted, private, eventID,
		); err != nil {
			return fmt.Errorf("sqlite storage: redact stored revision %q: %w", eventID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite storage: commit stored revision migration: %w", err)
	}
	return nil
}

func publicRevisionPayloads(ctx context.Context, tx *sql.Tx) (map[string][]byte, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT event_id, data_json
		FROM events
		WHERE event_type = ?
		  AND (private_data IS NULL OR length(private_data) = 0)
	`, workload.EventWorkloadRevisionCreated)
	if err != nil {
		return nil, fmt.Errorf("sqlite storage: read stored revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	payloads := map[string][]byte{}
	for rows.Next() {
		var eventID string
		var payload []byte
		if err := rows.Scan(&eventID, &payload); err != nil {
			return nil, fmt.Errorf("sqlite storage: read stored revisions: %w", err)
		}
		payloads[eventID] = payload
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite storage: read stored revisions: %w", err)
	}
	return payloads, nil
}

// splitStoredRevision is one stored event's payload as the two payloads it should
// have been written as.
func splitStoredRevision(eventID string, payload []byte) (redacted, private []byte, err error) {
	var stored struct {
		Revision domain.WorkloadRevision `json:"revision"`
	}
	if err := json.Unmarshal(payload, &stored); err != nil {
		return nil, nil, fmt.Errorf("sqlite storage: stored revision %q carries no readable revision: %w", eventID, err)
	}
	if stored.Revision.ID == "" {
		return nil, nil, fmt.Errorf("sqlite storage: stored revision %q names no revision", eventID)
	}
	redacted, err = json.Marshal(struct {
		Revision domain.PublicWorkloadRevision `json:"revision"`
	}{stored.Revision.Public()})
	if err != nil {
		return nil, nil, err
	}
	return redacted, payload, nil
}
