package scenario

import (
	"testing"
	"time"
)

func TestDashboardPlaybackStepsAndRewindsEverySubscriber(t *testing.T) {
	playback := NewDashboardPlayback()
	first, err := playback.Open(t.Context(), "ws_scenario", DashboardScenarioName, true)
	if err != nil {
		t.Fatalf("open first subscriber: %v", err)
	}
	initial := <-first
	if initial.Type != EmissionReset || initial.Reset == nil || initial.Reset.Playback.Cursor != 0 {
		t.Fatalf("initial emission = %+v", initial)
	}
	if err := playback.Command("ws_scenario", DashboardCommand{Type: CommandPause}); err != nil {
		t.Fatalf("pause scenario: %v", err)
	}
	paused := <-first
	if paused.Type != EmissionPlayback || paused.Playback == nil || paused.Playback.Status != PlaybackPaused {
		t.Fatalf("paused emission = %+v", paused)
	}

	second, err := playback.Open(t.Context(), "ws_scenario", DashboardScenarioName, false)
	if err != nil {
		t.Fatalf("open second subscriber: %v", err)
	}
	secondInitial := <-second
	if secondInitial.Reset == nil || secondInitial.Reset.Playback.Status != PlaybackPaused {
		t.Fatalf("second subscriber did not snap to current state: %+v", secondInitial)
	}
	if err := playback.Command("ws_scenario", DashboardCommand{Type: CommandNext}); err != nil {
		t.Fatalf("step scenario: %v", err)
	}
	for index, subscriber := range []<-chan DashboardEmission{first, second} {
		emission := <-subscriber
		if emission.Type != EmissionReset || emission.Reset == nil || emission.Reset.Playback.Cursor != 1 {
			t.Fatalf("subscriber %d step emission = %+v", index+1, emission)
		}
	}
	if err := playback.Command("ws_scenario", DashboardCommand{Type: CommandPrevious}); err != nil {
		t.Fatalf("rewind scenario: %v", err)
	}
	for index, subscriber := range []<-chan DashboardEmission{first, second} {
		emission := <-subscriber
		if emission.Reset == nil || emission.Reset.Playback.Cursor != 0 {
			t.Fatalf("subscriber %d rewind emission = %+v", index+1, emission)
		}
	}
}

func TestDashboardPlaybackDropsAStalledSubscriber(t *testing.T) {
	playback := NewDashboardPlayback()
	session := newDashboardPlaybackSession(DashboardTranscript{}, true)
	subscriber := make(chan DashboardEmission, 1)
	subscriber <- DashboardEmission{Type: EmissionPlayback}
	session.subscribers[subscriber] = struct{}{}
	playback.sessions["ws_stalled"] = session

	completed := make(chan error, 1)
	go func() {
		completed <- playback.Command("ws_stalled", DashboardCommand{Type: CommandPause})
	}()

	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("pause with stalled subscriber: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("stalled subscriber blocked playback command")
	}
	if _, exists := playback.sessions["ws_stalled"]; exists {
		t.Fatal("session with no live subscribers was retained")
	}
	<-subscriber
	if _, open := <-subscriber; open {
		t.Fatal("stalled subscriber channel remained open")
	}
}
