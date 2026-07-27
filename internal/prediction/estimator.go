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
//
// Whether the content is part of the key is a question about the stage rather than
// about the level, so every rung is asked it and every rung gets the same answer.
// A rung that dropped it would be a rung answering a stage about the content from
// launches of other content, which is the one thing a coarse rung may not
// generalize over: it is there to say that this machine resembles its neighbours.
//
// A stage priced from bytes has no ladder at any level, so a measured transfer is
// filed nowhere and answers nothing.
func levelKeys(identity domain.CandidateIdentity, stage domain.LaunchStage) []stageKey {
	if pricedFromBytes(stage) {
		return nil
	}
	content := contentStage(stage)
	return []stageKey{
		{key: identity.Candidate(content), stage: stage},
		{key: identity.ProviderAndRegion(content), stage: stage},
		{key: identity.ProviderKey(content), stage: stage},
	}
}

// levels are the three keyed levels in the order they are tried, which is the
// order levelKeys builds them in.
var levels = []domain.PredictionLevel{
	domain.LevelExactCandidate,
	domain.LevelProviderAndRegion,
	domain.LevelProvider,
}

// pricedFromBytes reports whether this stage's duration is a quantity of bytes
// over a throughput rather than a property of the candidate. Reading an image out
// of a registry, assembling it onto a disk, and reading the Run's declared inputs
// out of the object store are all of that shape, and none of them is answerable
// out of measured seconds.
//
// The bytes belong to the launch and not to the candidate. What a machine still
// has to move is whatever it does not already hold when the placement is taken, so
// one machine's own launches measured a transfer of one byte count and the next
// launch of it is a transfer of another: the machine that pulled forty gigabytes
// yesterday holds them today and moves nothing at all. An identity names the
// machine and the image and can name neither what is resident on the disk now nor
// which Artifact versions this Run consumes, so both launches land in one bucket
// and the measured seconds are served back as the price of a transfer that will
// not happen. That struck a host holding every byte out against a start bound for
// a fetch its own evidence priced at zero, and it charged a host holding the whole
// image for a pull it had already performed.
//
// What does recur about a transfer is the throughput of the path it crosses, and
// that is already learned where it belongs. An enrolled node measures the rate on
// the reads it really performs and publishes it as a fact with a validity window,
// the inventory answers for the bytes, and the two are multiplied at the moment of
// the decision. Seconds over a whole stage are the product with both halves thrown
// away, and a product measured once is not a measurement of either factor.
//
// The estimator learns a transfer again when the key names what a transfer is: the
// bytes this launch is missing and the path they cross. Until then a node's timed
// fetch is filed nowhere rather than filed wrong.
func pricedFromBytes(stage domain.LaunchStage) bool {
	switch stage {
	case domain.StageImageFetch, domain.StageUnpack, domain.StageArtifactFetch:
		return true
	default:
		return false
	}
}

// contentStage reports whether this stage's duration is a property of what the
// candidate was asked to run rather than of the candidate itself. An application
// coming up is, because the application is the image. What a machine spends being
// acquired, booting, enrolling, and creating a container is about the machine, and
// keying those on the content would split one machine's history across every image
// the fleet ever ran on it.
//
// It is the whole ladder's question and not one level's. Readiness is the only
// stage this fleet measures today, so a ladder that asked it at the narrowest rung
// alone would have had two of its three rungs answering every readiness in the
// fleet out of every other one.
func contentStage(stage domain.LaunchStage) bool {
	return stage == domain.StageApplicationReady
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
