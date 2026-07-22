package scenario

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrDashboardPlaybackNotFound = errors.New("dashboard playback session not found")
	ErrUnknownDashboardScenario  = errors.New("unknown dashboard scenario")
	ErrInvalidDashboardCommand   = errors.New("invalid dashboard playback command")
)

const (
	DashboardScenarioName = "full-schedule-forces-fresh-capacity"
	EmissionReset         = "reset"
	EmissionMessage       = "message"
	EmissionPlayback      = "playback"
	CommandPlay           = "play"
	CommandPause          = "pause"
	CommandPrevious       = "previous"
	CommandNext           = "next"
	CommandRestart        = "restart"
	CommandSetSpeed       = "set_speed"
	PlaybackPlaying       = "playing"
	PlaybackPaused        = "paused"
	PlaybackFinished      = "finished"
	dashboardTick         = 250 * time.Millisecond
)

type DashboardPlaybackSnapshot struct {
	Status         string `json:"status"`
	Cursor         int    `json:"cursor"`
	CueCount       int    `json:"cue_count"`
	ElapsedMillis  int    `json:"elapsed_millis"`
	DurationMillis int    `json:"duration_millis"`
	Speed          int    `json:"speed"`
}

type DashboardReset struct {
	Messages []DashboardMessage        `json:"messages"`
	Playback DashboardPlaybackSnapshot `json:"playback"`
	Fidelity DashboardFidelity         `json:"fidelity"`
}

type DashboardEmission struct {
	Type     string
	Reset    *DashboardReset
	Message  *DashboardMessage
	Playback *DashboardPlaybackSnapshot
}

func (e DashboardEmission) Data() any {
	switch e.Type {
	case EmissionReset:
		return e.Reset
	case EmissionMessage:
		return e.Message
	case EmissionPlayback:
		return e.Playback
	default:
		return nil
	}
}

type DashboardCommand struct {
	Type  string `json:"type"`
	Speed int    `json:"speed,omitempty"`
}

type DashboardPlayback struct {
	mu       sync.Mutex
	sessions map[string]*dashboardPlaybackSession
}

type dashboardPlaybackSession struct {
	transcript  DashboardTranscript
	state       DashboardPlaybackSnapshot
	observed    []DashboardMessage
	subscribers map[chan DashboardEmission]struct{}
	stop        chan struct{}
}

func NewDashboardPlayback() *DashboardPlayback {
	return &DashboardPlayback{sessions: map[string]*dashboardPlaybackSession{}}
}

func (p *DashboardPlayback) Open(ctx context.Context, workspaceID, scenarioName string, autoplay bool) (<-chan DashboardEmission, error) {
	if scenarioName != DashboardScenarioName {
		return nil, fmt.Errorf("%w %q", ErrUnknownDashboardScenario, scenarioName)
	}
	p.mu.Lock()
	session := p.sessions[workspaceID]
	if session == nil {
		transcript, err := BuildDashboardTranscript(ctx, workspaceID)
		if err != nil {
			p.mu.Unlock()
			return nil, err
		}
		session = newDashboardPlaybackSession(transcript, autoplay)
		p.sessions[workspaceID] = session
		go p.run(workspaceID, session)
	}
	subscriber := make(chan DashboardEmission, 1024)
	session.subscribers[subscriber] = struct{}{}
	subscriber <- session.reset()
	p.mu.Unlock()

	go func() {
		<-ctx.Done()
		p.unsubscribe(workspaceID, session, subscriber)
	}()
	return subscriber, nil
}

func newDashboardPlaybackSession(transcript DashboardTranscript, autoplay bool) *dashboardPlaybackSession {
	state := DashboardPlaybackSnapshot{
		Status:         PlaybackPlaying,
		CueCount:       len(transcript.Steps),
		DurationMillis: transcript.DurationMillis,
		Speed:          1,
	}
	var observed []DashboardMessage
	if !autoplay {
		state.Status = PlaybackFinished
		state.Cursor = len(transcript.Steps)
		state.ElapsedMillis = transcript.DurationMillis
		observed = messagesThrough(transcript, state.Cursor, time.Now())
	}
	return &dashboardPlaybackSession{
		transcript:  transcript,
		state:       state,
		observed:    observed,
		subscribers: map[chan DashboardEmission]struct{}{},
		stop:        make(chan struct{}),
	}
}

func (p *DashboardPlayback) Command(workspaceID string, command DashboardCommand) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	session := p.sessions[workspaceID]
	if session == nil {
		return fmt.Errorf("%w for Workspace %q", ErrDashboardPlaybackNotFound, workspaceID)
	}
	err := session.command(command)
	p.retireEmptySession(workspaceID, session)
	return err
}

func (p *DashboardPlayback) run(workspaceID string, session *dashboardPlaybackSession) {
	ticker := time.NewTicker(dashboardTick)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			p.mu.Lock()
			if p.sessions[workspaceID] == session {
				session.tick(now)
				if len(session.subscribers) == 0 {
					p.retireEmptySession(workspaceID, session)
					p.mu.Unlock()
					return
				}
			}
			p.mu.Unlock()
		case <-session.stop:
			return
		}
	}
}

func (p *DashboardPlayback) unsubscribe(workspaceID string, session *dashboardPlaybackSession, subscriber chan DashboardEmission) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessions[workspaceID] != session {
		return
	}
	if _, subscribed := session.subscribers[subscriber]; !subscribed {
		return
	}
	delete(session.subscribers, subscriber)
	close(subscriber)
	p.retireEmptySession(workspaceID, session)
}

func (p *DashboardPlayback) retireEmptySession(workspaceID string, session *dashboardPlaybackSession) {
	if p.sessions[workspaceID] != session || len(session.subscribers) != 0 {
		return
	}
	delete(p.sessions, workspaceID)
	close(session.stop)
}

func (s *dashboardPlaybackSession) command(command DashboardCommand) error {
	switch command.Type {
	case CommandPlay:
		if s.state.Status == PlaybackFinished {
			s.restart()
		}
		s.state.Status = PlaybackPlaying
		s.broadcast(DashboardEmission{Type: EmissionPlayback, Playback: snapshotPointer(s.state)})
	case CommandPause:
		s.state.Status = PlaybackPaused
		s.broadcast(DashboardEmission{Type: EmissionPlayback, Playback: snapshotPointer(s.state)})
	case CommandPrevious:
		if s.state.Cursor > 0 {
			s.state.Cursor--
		}
		s.state.Status = PlaybackPaused
		s.resetToCursor(time.Now())
	case CommandNext:
		if s.state.Cursor < len(s.transcript.Steps) {
			s.state.Cursor++
		}
		s.state.Status = PlaybackPaused
		if s.state.Cursor == len(s.transcript.Steps) {
			s.state.Status = PlaybackFinished
		}
		s.resetToCursor(time.Now())
	case CommandRestart:
		s.restart()
		s.broadcast(s.reset())
	case CommandSetSpeed:
		if command.Speed != 1 && command.Speed != 2 && command.Speed != 4 {
			return fmt.Errorf("%w: speed must be 1, 2, or 4", ErrInvalidDashboardCommand)
		}
		s.state.Speed = command.Speed
		s.broadcast(DashboardEmission{Type: EmissionPlayback, Playback: snapshotPointer(s.state)})
	default:
		return fmt.Errorf("%w: unknown type %q", ErrInvalidDashboardCommand, command.Type)
	}
	return nil
}

func (s *dashboardPlaybackSession) tick(now time.Time) {
	if s.state.Status != PlaybackPlaying {
		return
	}
	s.state.ElapsedMillis += int(dashboardTick/time.Millisecond) * s.state.Speed
	if s.state.ElapsedMillis > s.state.DurationMillis {
		s.state.ElapsedMillis = s.state.DurationMillis
	}
	for s.state.Cursor < len(s.transcript.Steps) && s.transcript.Steps[s.state.Cursor].AtMillis <= s.state.ElapsedMillis {
		message := observedDashboardMessage(s.transcript.Steps[s.state.Cursor].Message, now)
		s.observed = append(s.observed, message)
		s.state.Cursor++
		s.broadcast(DashboardEmission{Type: EmissionMessage, Message: &message})
	}
	if s.state.Cursor == len(s.transcript.Steps) {
		s.state.Status = PlaybackFinished
	}
	s.broadcast(DashboardEmission{Type: EmissionPlayback, Playback: snapshotPointer(s.state)})
}

func (s *dashboardPlaybackSession) restart() {
	s.state.Status = PlaybackPlaying
	s.state.Cursor = 0
	s.state.ElapsedMillis = 0
	s.observed = nil
}

func (s *dashboardPlaybackSession) resetToCursor(now time.Time) {
	s.observed = messagesThrough(s.transcript, s.state.Cursor, now)
	if s.state.Cursor == 0 {
		s.state.ElapsedMillis = 0
	} else {
		s.state.ElapsedMillis = s.transcript.Steps[s.state.Cursor-1].AtMillis
	}
	s.broadcast(s.reset())
}

func (s *dashboardPlaybackSession) reset() DashboardEmission {
	messages := append([]DashboardMessage(nil), s.transcript.Baseline...)
	messages = append(messages, s.observed...)
	return DashboardEmission{
		Type: EmissionReset,
		Reset: &DashboardReset{
			Messages: messages,
			Playback: s.state,
			Fidelity: s.transcript.Fidelity,
		},
	}
}

func (s *dashboardPlaybackSession) broadcast(emission DashboardEmission) {
	for subscriber := range s.subscribers {
		select {
		case subscriber <- emission:
		default:
			delete(s.subscribers, subscriber)
			close(subscriber)
		}
	}
}

func messagesThrough(transcript DashboardTranscript, cursor int, now time.Time) []DashboardMessage {
	messages := make([]DashboardMessage, 0, cursor)
	for _, step := range transcript.Steps[:cursor] {
		messages = append(messages, observedDashboardMessage(step.Message, now))
	}
	return messages
}

func observedDashboardMessage(message DashboardMessage, now time.Time) DashboardMessage {
	if message.Event == nil {
		return message
	}
	event := *message.Event
	event.Time = now.UTC().Format(time.RFC3339Nano)
	message.Event = &event
	return message
}

func snapshotPointer(snapshot DashboardPlaybackSnapshot) *DashboardPlaybackSnapshot {
	copy := snapshot
	return &copy
}
