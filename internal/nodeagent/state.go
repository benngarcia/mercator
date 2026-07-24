package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/node"
)

// State is the agent's local durable memory. It exists for one reason: an
// agent that restarts must be able to say "I already did that" about a command
// it is handed again. Without it, every redelivered launch is a second
// container.
//
// It also holds the session credential, so a restarted agent resumes rather
// than re-enrolling, and the spool of events it owes the control plane.
type State struct {
	mu   sync.Mutex
	path string
	data stateFile
}

type stateFile struct {
	NodeID         string       `json:"node_id"`
	SessionToken   string       `json:"session_token,omitempty"`
	SessionExpires time.Time    `json:"session_expires,omitzero"`
	FencingToken   uint64       `json:"fencing_token"`
	Applied        []string     `json:"applied_operation_ids,omitempty"`
	Spool          []node.Event `json:"spool,omitempty"`
	EventSequence  uint64       `json:"event_sequence"`
}

// OpenState loads the agent's state from path, creating it when absent.
func OpenState(path, nodeID string) (*State, error) {
	state := &State{path: path, data: stateFile{NodeID: nodeID}}
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return state, state.persist()
	case err != nil:
		return nil, fmt.Errorf("read node agent state: %w", err)
	}
	if err := json.Unmarshal(raw, &state.data); err != nil {
		return nil, fmt.Errorf("decode node agent state at %s: %w", path, err)
	}
	if state.data.NodeID != nodeID {
		return nil, fmt.Errorf(
			"node agent state at %s belongs to node %q, not %q; a machine cannot adopt another node's memory",
			path, state.data.NodeID, nodeID,
		)
	}
	return state, nil
}

// Session returns a session credential that has not expired at now.
func (state *State) Session(now time.Time) (string, uint64, bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.data.SessionToken == "" || !now.UTC().Before(state.data.SessionExpires) {
		return "", 0, false
	}
	return state.data.SessionToken, state.data.FencingToken, true
}

// Enrolled records a fresh enrollment. A new fencing token means the control
// plane has superseded whatever this agent was doing, so the applied-operation
// memory stays and the session is replaced.
func (state *State) Enrolled(enrollment capability.Enrollment) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.data.SessionToken = enrollment.SessionToken
	state.data.SessionExpires = enrollment.SessionExpires.UTC()
	state.data.FencingToken = enrollment.FencingToken
	return state.persistLocked()
}

func (state *State) FencingToken() uint64 {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.data.FencingToken
}

// Applied reports whether this agent already performed one operation.
func (state *State) Applied(operationID string) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	return slices.Contains(state.data.Applied, operationID)
}

// MarkApplied records an operation as performed, durably, before its result is
// reported. A crash between the two costs a duplicate acknowledgement, never a
// duplicate container.
func (state *State) MarkApplied(operationID string) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if slices.Contains(state.data.Applied, operationID) {
		return nil
	}
	state.data.Applied = append(state.data.Applied, operationID)
	return state.persistLocked()
}

// Spool holds an event the control plane could not be told about. It keeps its
// ID, so replaying it after a reconnection changes nothing.
func (state *State) Spool(event node.Event) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, spooled := range state.data.Spool {
		if spooled.ID == event.ID {
			return nil
		}
	}
	state.data.Spool = append(state.data.Spool, event)
	return state.persistLocked()
}

func (state *State) Spooled() []node.Event {
	state.mu.Lock()
	defer state.mu.Unlock()
	return slices.Clone(state.data.Spool)
}

func (state *State) ClearSpool() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.data.Spool = nil
	return state.persistLocked()
}

// NextEventID mints a stable, monotonic identity for one event, so the same
// fact carries the same ID however many times it is delivered.
func (state *State) NextEventID() string {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.data.EventSequence++
	_ = state.persistLocked()
	return state.data.NodeID + "-" + strconv.FormatUint(state.data.EventSequence, 10)
}

func (state *State) persist() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.persistLocked()
}

// persistLocked writes through a temporary file and renames it, so a crash
// mid-write leaves the previous state rather than a truncated one.
func (state *State) persistLocked() error {
	encoded, err := json.Marshal(state.data)
	if err != nil {
		return fmt.Errorf("encode node agent state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(state.path), 0o700); err != nil {
		return fmt.Errorf("create node agent state directory: %w", err)
	}
	temporary := state.path + ".writing"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("write node agent state: %w", err)
	}
	if err := os.Rename(temporary, state.path); err != nil {
		return fmt.Errorf("commit node agent state: %w", err)
	}
	return nil
}
