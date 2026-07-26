// Package prediction answers what a launch stage will cost from what earlier
// launches of the same thing really spent.
//
// The whole difficulty is what "the same thing" means. A history is only worth
// reading if the key it is filed under recurs, and the ID a listing arrives with
// recurs for one backend and never for another: a Vast ask ID is a fresh integer
// for every search of a machine that was already there, so a store keyed on it
// would fill with one-sample keys nothing can ever read back and would report
// each of them as candidate-specific evidence. domain.CandidateIdentity is the
// key that does recur, and this package never sees an offer snapshot ID at all.
//
// The second difficulty is that most candidates have no history. A fleet that
// answered only where it had samples would have nothing to say about a machine
// it has never used, which is most of a marketplace, so the answer falls through
// declared levels: this exact candidate, then this provider in this region, then
// this provider, then the prior the rest of the tree already computes. Every
// answer names the level it came from and the number of launches behind it,
// because an answer that does not say what it rests on cannot be audited.
package prediction

import (
	"math"
	"slices"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// Observation is one launch stage this fleet measured, filed under what the
// candidate was rather than under what its listing was called.
//
// Seconds is what the world spent, read from moments Mercator itself observed.
// A stage nobody could time produces no Observation at all: a zero here is a
// stage that really took no time, and a store that recorded silence as zero
// would teach itself that every unmeasured launch is instant.
type Observation struct {
	Candidate  domain.CandidateIdentity
	Stage      domain.LaunchStage
	Seconds    float64
	ObservedAt time.Time
}

// History is every observation this fleet has, indexed under each level of the
// hierarchy that can answer about it. One observation is filed under all of the
// levels it belongs to, which is what lets a candidate nobody has measured be
// answered out of its neighbours.
type History struct {
	samples map[stageKey][]float64
}

// stageKey is one bucket of samples: a key from some level of the hierarchy, and
// the stage those samples are of. The stage is part of the bucket because the
// stages of a launch are different work with different causes, and a store that
// mixed them would answer a pull with a boot.
type stageKey struct {
	key   string
	stage domain.LaunchStage
}

// NewHistory indexes observations for reading. Order does not matter and is not
// kept: a quantile is a property of the set, and the same set has to produce the
// same answer however the events arrived.
func NewHistory(observations []Observation) History {
	history := History{samples: map[stageKey][]float64{}}
	for _, observation := range observations {
		for _, key := range levelKeys(observation.Candidate, observation.Stage) {
			if key.key == "" {
				continue
			}
			history.samples[key] = append(history.samples[key], observation.Seconds)
		}
	}
	for key := range history.samples {
		slices.Sort(history.samples[key])
	}
	return history
}

// Empty reports whether this fleet has measured anything at all. A control plane
// with no history answers every stage from the prior, which is what the whole
// tree did before this package existed.
func (history History) Empty() bool { return len(history.samples) == 0 }

// levelKeys is the ladder for one candidate and one stage, narrowest first. A
// level a candidate cannot produce a key for is named as the empty string and
// skipped by both the writer and the reader, so a provider that publishes no
// region does not file every machine it sells under one blank place.
func levelKeys(identity domain.CandidateIdentity, stage domain.LaunchStage) []stageKey {
	return []stageKey{
		{key: identity.Candidate(contentStage(stage)), stage: stage},
		{key: identity.ProviderAndRegion(), stage: stage},
		{key: identity.ProviderKey(), stage: stage},
	}
}

// levels are the three keyed levels in the order they are tried, which is the
// order levelKeys builds them in.
var levels = []domain.PredictionLevel{
	domain.LevelExactCandidate,
	domain.LevelProviderAndRegion,
	domain.LevelProvider,
}

// contentStage reports whether this stage's duration is a property of what the
// candidate was asked to run rather than of the candidate itself. Fetching and
// unpacking an image are about the image, and so is an application coming up,
// because the application is the image. What a machine spends being acquired,
// booting, enrolling, and creating a container is about the machine, and keying
// those on the content would split one machine's history across every image the
// fleet ever ran on it.
func contentStage(stage domain.LaunchStage) bool {
	switch stage {
	case domain.StageImageFetch, domain.StageUnpack, domain.StageArtifactFetch, domain.StageApplicationReady:
		return true
	default:
		return false
	}
}

// Answer is what a level of the hierarchy had to say about one stage. A level
// with no samples answers nothing and the caller falls through to the next; the
// prior is the last one and it is the caller's own, because this package has no
// opinion about a stage nobody has measured.
type Answer struct {
	Level       domain.PredictionLevel
	Key         string
	SampleCount int
	P50         float64
	P90         float64
	Confidence  float64
}

// Answered reports whether this answer came from measured launches.
func (answer Answer) Answered() bool { return answer.SampleCount > 0 }

// Estimate is the record's form of this answer, carrying the level and the key
// beside the seconds so a reader can check one against the other.
func (answer Answer) Estimate(modelVersion string) domain.Estimate {
	return domain.Estimate{
		Expected:     answer.P50,
		P50:          answer.P50,
		P90:          answer.P90,
		Confidence:   answer.Confidence,
		Source:       Source,
		SampleCount:  answer.SampleCount,
		ModelVersion: modelVersion,
		Level:        answer.Level,
		Key:          answer.Key,
	}
}

// Source is what a stage answered from history names as its evidence. It is one
// word for every level, because which level answered is recorded beside it and a
// source that repeated the level would be two spellings of one fact.
const Source = "history"

// Predict is the ladder walked for one candidate and one stage: the narrowest
// level holding any samples answers, and a fleet that has measured nothing about
// this candidate at any level answers nothing at all.
func (history History) Predict(identity domain.CandidateIdentity, stage domain.LaunchStage) Answer {
	for index, key := range levelKeys(identity, stage) {
		samples := history.samples[key]
		if key.key == "" || len(samples) == 0 {
			continue
		}
		return Answer{
			Level:       levels[index],
			Key:         key.key,
			SampleCount: len(samples),
			P50:         quantile(samples, 0.5),
			P90:         quantile(samples, 0.9),
			Confidence:  confidence(levels[index], len(samples)),
		}
	}
	return Answer{Level: domain.LevelPrior}
}

// ExactCandidateConfidence, ProviderAndRegionConfidence, and ProviderConfidence
// are what a full history at each level is worth. They decline with the level
// because a coarser level answers about other machines: a region's samples are
// about the same provider in the same place and nothing more, and a provider's
// are about every product it sells, so an answer from one is evidence that this
// candidate resembles them rather than evidence about this candidate.
//
// None of the three is 1. A history is a sample of a distribution and the next
// launch is a draw from it, so even a machine measured a hundred times can be
// slow the next time, and a predictor certain of its own p50 would have the
// score charge no doubt at all for a stage that is only ever an expectation.
const (
	ExactCandidateConfidence    = 0.9
	ProviderAndRegionConfidence = 0.6
	ProviderConfidence          = 0.4
)

// confidence is what an answer at one level from this many launches is worth.
//
// The sample count shrinks it toward nothing on the classic terms: one launch is
// worth half of what the level is worth, two are worth two thirds, and a level
// approaches its own figure as the launches accumulate. The alternative is a
// threshold, which would make the answer from four launches worthless and the
// answer from five authoritative for a reason nothing measured.
func confidence(level domain.PredictionLevel, samples int) float64 {
	base := ProviderConfidence
	switch level {
	case domain.LevelExactCandidate:
		base = ExactCandidateConfidence
	case domain.LevelProviderAndRegion:
		base = ProviderAndRegionConfidence
	}
	return base * float64(samples) / float64(samples+1)
}

// quantile is the empirical quantile of a sorted sample set, interpolating
// between the two order statistics it falls between.
//
// Interpolation rather than nearest rank, because a duration is continuous and
// two launches of ninety and a hundred and fifty seconds say the middle of this
// machine's distribution is somewhere between them. Nearest rank would answer
// with one of the two observations, which reads as a measurement of something
// that never happened.
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	position := q * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	return sorted[lower] + (position-float64(lower))*(sorted[upper]-sorted[lower])
}
