package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/benngarcia/mercator/internal/domain"
)

// A Run used to state a placement objective, which named a quantity to minimise
// and left unstated what a second of it was worth. It states a ServiceClass now,
// which carries the exchange rate the score is computed over. There is no shim
// and nothing derived between them, so events written before the change carry a
// word nothing reads and every projection over them would show a Run with no
// class at all.
//
// This migration rewrites those events in place. It is a rename of a field whose
// value maps one-for-one onto the vocabulary that replaced it, which is the only
// kind of rewrite an append-only log may take: no decision changes, and no
// decision is superseded, because the class each objective becomes prices waiting
// the way that objective ranked it.
//
// It refuses to run while any Run carrying the old field is still open. A Run
// that is mid-flight is a Run whose next event Mercator is about to append from
// state it has already read, and rewriting the record underneath it would leave
// the two halves of one stream stating different vocabularies. A closed stream is
// history, and history is what this is safe on.

// legacyObjectiveClasses is what each objective becomes, and the whole of the
// mapping. cheapest becomes batch, which prices waiting at a fifth of the
// machine's rent, so cost still dominates. balanced becomes standard, which
// prices it at exactly that rent, which is the rate balanced already presumed.
// fastest_start becomes interactive, the only class that prices the start rather
// than the finish. fastest_completion becomes experimental, which prices the
// finish and pays over the odds to get there. Nothing becomes opportunistic:
// no objective could say that waiting is free, so no historical Run said it.
var legacyObjectiveClasses = map[string]domain.ServiceClass{
	"cheapest":           domain.ClassBatch,
	"balanced":           domain.ClassStandard,
	"fastest_start":      domain.ClassInteractive,
	"fastest_completion": domain.ClassExperimental,
}

// legacyObjectiveSites is every place an event carries a placement objective:
// the workload a Run was requested with, public and private, the policy a
// Booking Decision was taken under, and the workload revision a caller stored
// for later Runs to name. The stream each site lives on is part of the site,
// because only one of those streams can still be in flight.
//
// The stored revision is the site whose omission repriced work rather than only
// leaving a stale word behind. Nothing decodes `objective` any more, so a
// revision left speaking the old vocabulary reads back with no class at all, and
// the next Run created from it is normalised to standard: a caller who stored
// fastest_start would be scored at a twentieth of interactive's rate with
// nothing in the record saying so, which is the silent repricing this migration
// exists to refuse.
type legacyObjectiveSite struct {
	stream    string
	column    string
	objective string
	class     string
}

var legacyObjectiveSites = []legacyObjectiveSite{
	{"run", "data_json", "$.workload_revision.spec.placement.objective", "$.workload_revision.spec.placement.service_class"},
	{"run", "private_data", "$.workload_revision.spec.placement.objective", "$.workload_revision.spec.placement.service_class"},
	{"run", "data_json", "$.decision.policy.objective", "$.decision.policy.service_class"},
	{"workload", "data_json", "$.revision.spec.placement.objective", "$.revision.spec.placement.service_class"},
}

func migrateLegacyPlacementObjectives(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite storage: begin service class migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := refuseOpenLegacyObjectives(ctx, tx); err != nil {
		return err
	}
	if err := refuseUnmappableObjectives(ctx, tx); err != nil {
		return err
	}
	if err := recordTheWeightsHistoryWasScoredAt(ctx, tx); err != nil {
		return err
	}
	for _, site := range legacyObjectiveSites {
		if err := renameObjectiveToServiceClass(ctx, tx, site); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite storage: commit service class migration: %w", err)
	}
	return nil
}

// refuseOpenLegacyObjectives stops the migration while a Run that stated an
// objective is still open. Such a Run's next event is appended from state
// Mercator has already read, so rewriting its history now leaves one stream
// speaking two vocabularies with nothing to say which half is authoritative.
//
// It asks about the run streams only, because a Run is the only thing here that
// can be in flight. A stored workload revision is a definition rather than a
// life cycle: no event is ever appended from state read out of one, and a Run
// created from it holds its own copy of the revision on its own stream, which is
// the copy this refusal protects. A workload stream never closes, so counting it
// as open would refuse every database that has one.
func refuseOpenLegacyObjectives(ctx context.Context, tx *sql.Tx) error {
	var open bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM events AS legacy
			WHERE legacy.stream_type = 'run'
			  AND `+anyObjectivePresent()+`
			  AND NOT EXISTS (
				SELECT 1
				FROM events AS closed
				WHERE closed.workspace_id = legacy.workspace_id
				  AND closed.stream_type = legacy.stream_type
				  AND closed.stream_id = legacy.stream_id
				  AND closed.event_type = 'compute.run.closed.v1'
			  )
		)
	`).Scan(&open); err != nil {
		return fmt.Errorf("sqlite storage: inspect legacy placement objectives: %w", err)
	}
	if open {
		return fmt.Errorf("sqlite storage: cannot migrate open legacy placement objectives")
	}
	return nil
}

// refuseUnmappableObjectives stops the migration on an objective this mapping
// does not know. Guessing a class for it would price a historical Run's waiting
// at a rate nobody chose, and a decision record is not a place to guess.
func refuseUnmappableObjectives(ctx context.Context, tx *sql.Tx) error {
	for _, site := range legacyObjectiveSites {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT DISTINCT json_extract(%[1]s, '%[2]s') FROM events
			 WHERE stream_type = '%[3]s' AND json_type(%[1]s, '%[2]s') = 'text'`,
			site.column, site.objective, site.stream,
		))
		if err != nil {
			return fmt.Errorf("sqlite storage: read legacy placement objectives: %w", err)
		}
		objectives, err := scanStrings(rows)
		if err != nil {
			return fmt.Errorf("sqlite storage: read legacy placement objectives: %w", err)
		}
		for _, objective := range objectives {
			if _, known := legacyObjectiveClasses[objective]; !known {
				return fmt.Errorf("sqlite storage: legacy placement objective %q has no service class", objective)
			}
		}
	}
	return nil
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	defer func() { _ = rows.Close() }()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// recordTheWeightsHistoryWasScoredAt writes onto each old decision the exchange
// rates it was actually scored at, which is what makes the whole log reproducible
// rather than only the part of it written since a class carried a rate.
//
// Those rates were nearly all zero, and that is the fact being recorded. Nothing
// populated the weights in production: the balanced objective got one default,
// what a second of waiting cost the machine doing the waiting, and every other
// objective was scored on price alone with its time and uncertainty terms
// multiplied by zero. So a decision migrated to interactive carries a class saying
// somebody was watching and weights saying nobody priced it, which is exactly what
// happened and exactly why the class replaced the objective.
//
// It runs before the rename, while the objective is still there to read.
func recordTheWeightsHistoryWasScoredAt(ctx context.Context, tx *sql.Tx) error {
	statement := fmt.Sprintf(`
		UPDATE events
		SET data_json = json_set(
			data_json,
			'$.decision.weights',
			CASE json_extract(data_json, '$.decision.policy.objective')
			  WHEN 'balanced' THEN json_object('start_latency_usd_per_second', %v)
			  ELSE json_object()
			END
		)
		WHERE json_type(data_json, '$.decision.policy.objective') = 'text'
		  AND json_type(data_json, '$.decision.weights') IS NULL
	`, domain.WaitingUSDPerSecond)
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("sqlite storage: record historical score weights: %w", err)
	}
	return nil
}

// renameObjectiveToServiceClass writes the class beside the objective and removes
// the objective, in one statement per site so a partly rewritten event never
// exists. The mapping is a CASE built from the one table above rather than
// spelled out here, so there is one place a class assignment can be argued with.
func renameObjectiveToServiceClass(ctx context.Context, tx *sql.Tx, site legacyObjectiveSite) error {
	statement := fmt.Sprintf(`
		UPDATE events
		SET %[1]s = json_remove(json_set(%[1]s, '%[3]s', %[4]s), '%[2]s')
		WHERE stream_type = '%[5]s'
		  AND json_type(%[1]s, '%[2]s') = 'text'
	`, site.column, site.objective, site.class, objectiveCase(site.column, site.objective), site.stream)
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("sqlite storage: migrate placement objective at %s: %w", site.objective, err)
	}
	return nil
}

func objectiveCase(column, objectivePath string) string {
	var when strings.Builder
	when.WriteString(fmt.Sprintf("CASE json_extract(%s, '%s')", column, objectivePath))
	for _, objective := range sortedObjectives() {
		when.WriteString(fmt.Sprintf(" WHEN '%s' THEN '%s'", objective, legacyObjectiveClasses[objective]))
	}
	// Every objective still in the log has a class by the time this runs, because
	// refuseUnmappableObjectives has already looked. The else branch keeps the word
	// that was there rather than inventing one, so a row that somehow reached here
	// fails the next validation loudly instead of being quietly repriced.
	when.WriteString(fmt.Sprintf(" ELSE json_extract(%s, '%s') END", column, objectivePath))
	return when.String()
}

func sortedObjectives() []string {
	return slices.Sorted(maps.Keys(legacyObjectiveClasses))
}

// anyObjectivePresent is the condition that an event still speaks the old
// vocabulary anywhere, over the row aliased as legacy. Which streams it is asked
// about is the caller's to say.
func anyObjectivePresent() string {
	conditions := make([]string, 0, len(legacyObjectiveSites))
	for _, site := range legacyObjectiveSites {
		conditions = append(conditions, fmt.Sprintf("json_type(legacy.%s, '%s') = 'text'", site.column, site.objective))
	}
	return "(" + strings.Join(conditions, " OR ") + ")"
}
