package latency

import (
	"sort"
	"sync"
	"time"

	"github.com/bengarcia/mercator/internal/domain"
)

type Observation struct {
	WorkspaceID     string
	OfferSnapshotID string
	StartSeconds    float64
	ObservedAt      time.Time
}

type Estimator struct {
	mu           sync.Mutex
	observations map[string][]Observation
}

func New() *Estimator {
	return &Estimator{observations: map[string][]Observation{}}
}

func (e *Estimator) Record(observation Observation) {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := observation.WorkspaceID + "/" + observation.OfferSnapshotID
	e.observations[key] = append(e.observations[key], observation)
}

func (e *Estimator) Estimate(workspaceID, offerSnapshotID string) domain.Estimate {
	e.mu.Lock()
	defer e.mu.Unlock()
	values := append([]Observation(nil), e.observations[workspaceID+"/"+offerSnapshotID]...)
	if len(values) == 0 {
		return domain.Estimate{Source: "latency_estimator", ModelVersion: "latency-v1"}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].StartSeconds < values[j].StartSeconds })
	sum := 0.0
	for _, value := range values {
		sum += value.StartSeconds
	}
	p90Index := int(0.9*float64(len(values)-1) + 0.5)
	return domain.Estimate{
		P50:          values[len(values)/2].StartSeconds,
		P90:          values[p90Index].StartSeconds,
		Expected:     sum / float64(len(values)),
		Source:       "latency_estimator",
		SampleCount:  len(values),
		ModelVersion: "latency-v1",
	}
}
