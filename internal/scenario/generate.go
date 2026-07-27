package scenario

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

type GeneratorConfig struct {
	Seed             string
	ArrivalType      ArrivalType
	RunCount         int
	RentalCount      int
	MarketplaceCount int
	ImageCount       int
	ArtifactCount    int
	IncludeFaults    bool
}

type GenerationSample struct {
	Key   string `json:"key"`
	Value uint64 `json:"value"`
}

type blueprintGenerator struct {
	seed    string
	samples []GenerationSample
}

func GenerateBlueprint(config GeneratorConfig) (Blueprint, []GenerationSample, error) {
	config = defaultGeneratorConfig(config)
	if err := validateGeneratorConfig(config); err != nil {
		return Blueprint{}, nil, err
	}
	generator := &blueprintGenerator{seed: config.Seed}
	images, imageRefs := generator.images(config.ImageCount)
	artifacts := generator.artifacts(config.ArtifactCount, generatedArtifactProducer(config))
	rentals := generator.rentals(config.RentalCount, imageRefs, images, artifacts)
	marketplace := generator.marketplace(config.MarketplaceCount)
	candidateIDs := generatedCandidateIDs(rentals, marketplace)
	arrivals := generator.arrivals(config, imageRefs, artifacts)
	runs, err := arrivals.ExpandedRuns()
	if err != nil {
		return Blueprint{}, nil, err
	}
	blueprint := Blueprint{
		Schema:         BlueprintSchemaV1,
		Name:           "generated-" + shortGeneratedDigest(config.Seed, "name"),
		Summary:        "Deterministic generated Mercator Lab world.",
		Classification: ClassificationGreen,
		Kind:           KindGenerated,
		Seed:           config.Seed,
		World: WorldSpec{
			Clock:         time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC),
			Images:        images,
			Artifacts:     artifacts,
			Rentals:       rentals,
			Marketplace:   marketplace,
			Paths:         generator.paths(candidateIDs),
			RuntimeModels: generator.runtimeModels(runs, candidateIDs),
		},
		Arrivals: &arrivals,
	}
	if config.IncludeFaults {
		blueprint.Faults = generator.faults(runs)
	}
	if err := blueprint.validate(); err != nil {
		return Blueprint{}, nil, fmt.Errorf("generated Blueprint is invalid: %w", err)
	}
	return blueprint, append([]GenerationSample(nil), generator.samples...), nil
}

func defaultGeneratorConfig(config GeneratorConfig) GeneratorConfig {
	if config.ArrivalType == "" {
		config.ArrivalType = ArrivalFixed
	}
	if config.RunCount == 0 {
		config.RunCount = 3
	}
	if config.RentalCount == 0 {
		config.RentalCount = 2
	}
	if config.MarketplaceCount == 0 {
		config.MarketplaceCount = 2
	}
	if config.ImageCount == 0 {
		config.ImageCount = 2
	}
	if config.ArtifactCount == 0 {
		config.ArtifactCount = 1
	}
	return config
}

func validateGeneratorConfig(config GeneratorConfig) error {
	if config.Seed == "" {
		return fmt.Errorf("Blueprint generator seed is required")
	}
	if config.RunCount < 1 ||
		config.RentalCount < 1 ||
		config.MarketplaceCount < 1 ||
		config.ImageCount < 1 ||
		config.ArtifactCount < 1 {
		return fmt.Errorf("Blueprint generator counts must be positive")
	}
	switch config.ArrivalType {
	case ArrivalFixed, ArrivalPeriodic, ArrivalBurst:
		return nil
	default:
		return fmt.Errorf("Blueprint generator does not support arrival type %q", config.ArrivalType)
	}
}

func (generator *blueprintGenerator) images(count int) (map[string]ImageSpec, []string) {
	images := make(map[string]ImageSpec, count)
	refs := make([]string, count)
	sharedDigest := "sha256:" + generatedDigest(generator.seed, "layer/shared")
	for index := range count {
		ref := fmt.Sprintf(
			"registry.example/mercator/generated-%d@sha256:%s",
			index+1,
			generatedDigest(generator.seed, fmt.Sprintf("image/%d", index)),
		)
		refs[index] = ref
		images[ref] = ImageSpec{Layers: []LayerSpec{
			{Digest: sharedDigest, Size: ByteSize(generator.megabytes(fmt.Sprintf("image/%d/shared-size", index), 64, 512))},
			{
				Digest: "sha256:" + generatedDigest(generator.seed, fmt.Sprintf("layer/%d/unique", index)),
				Size:   ByteSize(generator.megabytes(fmt.Sprintf("image/%d/unique-size", index), 8, 128)),
			},
		}}
	}
	return images, refs
}

// artifacts builds the catalog. The first version is what the generated DAG's
// producer publishes, so it is durable only once that Run runs; every other
// version was published before this world started, which is what lets a machine
// be seeded holding a copy of it.
func (generator *blueprintGenerator) artifacts(count int, producer string) []ArtifactSpec {
	artifacts := make([]ArtifactSpec, count)
	for index := range count {
		artifacts[index] = ArtifactSpec{
			ID:            fmt.Sprintf("artifact:generated:%03d:v1", index+1),
			ContentDigest: "sha256:" + generatedDigest(generator.seed, fmt.Sprintf("artifact/%d/content", index)),
			Size:          ByteSize(generator.megabytes(fmt.Sprintf("artifact/%d/size", index), 32, 1024)),
		}
	}
	if producer != "" {
		artifacts[0].ProducedBy = producer
	}
	return artifacts
}

// generatedArtifactProducer names the Run that publishes the first Artifact.
// Only the fixed arrival plan builds a producer/consumer DAG; a periodic or
// burst family is the same request repeated, and an Artifact every arrival
// claimed to publish would have as many producers as it has arrivals.
func generatedArtifactProducer(config GeneratorConfig) string {
	if config.ArrivalType != ArrivalFixed {
		return ""
	}
	return "generated-001"
}

func (generator *blueprintGenerator) rentals(count int, imageRefs []string, images map[string]ImageSpec, artifacts []ArtifactSpec) []RentalSpec {
	rentals := make([]RentalSpec, count)
	regions := []string{"us-west", "us-east", "eu-central"}
	for index := range count {
		id := fmt.Sprintf("rental-generated-%03d", index+1)
		minimum := Duration(time.Duration(30+generator.draw("rental/"+id+"/minimum", 91)) * time.Second)
		rental := RentalSpec{
			ID:             id,
			Region:         regions[generator.draw("rental/"+id+"/region", uint64(len(regions)))],
			RatePerHourUSD: generator.price("rental/"+id+"/price", 100, 500),
			Billing: BillingSpec{
				SetupFeeUSD:   generator.price("rental/"+id+"/setup", 0, 50),
				MinimumCharge: &minimum,
			},
			Resources:   generatedResources(index),
			CacheMounts: []HeldCacheSpec{{Name: "build-cache", CompatibilityKey: "generated-v1", Size: ByteSize(2e9)}},
		}
		if index == 0 {
			rental.CachedLayers = []string{images[imageRefs[0]].Layers[0].Digest}
		}
		// A machine can only be found holding content the object store already
		// published, so a seeded replica names a version this world's own DAG
		// does not produce.
		if seeded := seededArtifact(artifacts, index); seeded != "" {
			rental.ArtifactReplicas = []ArtifactReplicaSpec{{
				Artifact: seeded,
				State:    domain.ArtifactReplicaVerified,
			}}
		}
		rentals[index] = rental
	}
	return rentals
}

// seededArtifact picks the prepublished version this machine holds a copy of,
// and answers empty when the catalog has none to give it.
func seededArtifact(artifacts []ArtifactSpec, index int) string {
	if index == 0 {
		return ""
	}
	for offset := range artifacts {
		artifact := artifacts[(index+offset)%len(artifacts)]
		if artifact.Prepublished() {
			return artifact.ID
		}
	}
	return ""
}

func (generator *blueprintGenerator) marketplace(count int) []MarketplaceOfferSpec {
	offers := make([]MarketplaceOfferSpec, count)
	regions := []string{"us-west", "us-east", "eu-central"}
	for index := range count {
		id := fmt.Sprintf("market-generated-%03d", index+1)
		available := index == 0 || generator.draw("market/"+id+"/available", 2) == 1
		expected := Duration(time.Duration(30+generator.draw("market/"+id+"/provision", 271)) * time.Second)
		p90 := Duration(expected.Duration() + time.Duration(30+generator.draw("market/"+id+"/p90", 301))*time.Second)
		minimum := Duration(time.Duration(30+generator.draw("market/"+id+"/minimum", 271)) * time.Second)
		// A generated machine really spends its stages, drawn independently of the
		// expectation its provider published: a generator whose actual equalled its
		// own claim would make every start prediction right by construction.
		acquisition := Duration(time.Duration(generator.draw("market/"+id+"/acquisition", 61)) * time.Second)
		boot := Duration(time.Duration(15+generator.draw("market/"+id+"/boot", 121)) * time.Second)
		agentReady := Duration(time.Duration(5+generator.draw("market/"+id+"/agent", 31)) * time.Second)
		offers[index] = MarketplaceOfferSpec{
			ID:             id,
			Provider:       "generated-cloud",
			Region:         regions[generator.draw("market/"+id+"/region", uint64(len(regions)))],
			Available:      &available,
			RatePerHourUSD: generator.price("market/"+id+"/price", 100, 600),
			Billing: BillingSpec{
				SetupFeeUSD:   generator.price("market/"+id+"/setup", 0, 100),
				MinimumCharge: &minimum,
			},
			Provisioning: ProvisioningSpec{
				Expected:    expected,
				P90:         &p90,
				Acquisition: &acquisition,
				Boot:        &boot,
				AgentReady:  &agentReady,
			},
			Resources: generatedResources(index + 1),
		}
	}
	return offers
}

func (generator *blueprintGenerator) arrivals(config GeneratorConfig, imageRefs []string, artifacts []ArtifactSpec) ArrivalPlan {
	request := generator.request(imageRefs[0])
	switch config.ArrivalType {
	case ArrivalPeriodic:
		return ArrivalPlan{
			Type: ArrivalPeriodic,
			Periodic: &RunFamilySpec{
				NamePrefix: "periodic",
				At:         0,
				Interval:   Duration(time.Minute),
				Count:      config.RunCount,
				Request:    request,
			},
		}
	case ArrivalBurst:
		return ArrivalPlan{
			Type: ArrivalBurst,
			Burst: &RunFamilySpec{
				NamePrefix: "burst",
				At:         Duration(time.Minute),
				Interval:   0,
				Count:      config.RunCount,
				Request:    request,
			},
		}
	default:
		runs := make([]RunArrivalSpec, config.RunCount)
		for index := range runs {
			runRequest := generator.request(imageRefs[index%len(imageRefs)])
			if index == 0 {
				runRequest.ProducesArtifacts = []string{artifacts[0].ID}
			}
			if index == 1 {
				runRequest.ConsumesArtifacts = []string{artifacts[0].ID}
			}
			runs[index] = RunArrivalSpec{
				Name:    fmt.Sprintf("generated-%03d", index+1),
				At:      Duration(time.Duration(index/2) * time.Minute),
				Request: runRequest,
			}
		}
		return ArrivalPlan{Type: ArrivalFixed, Runs: runs}
	}
}

func (generator *blueprintGenerator) request(image string) RequestSpec {
	expected := Duration(5 * time.Minute)
	maximum := Duration(10 * time.Minute)
	return RequestSpec{
		Image:           image,
		Resources:       generatedRunResources(),
		ExpectedRuntime: &expected,
		MaxRuntime:      &maximum,
		ServiceClass:    string(domain.ClassStandard),
		CacheMounts:     []CacheMountSpec{{Name: "build-cache", CompatibilityKey: "generated-v1", Size: ByteSize(2e9)}},
		Phases: []WorkloadPhaseSpec{
			{Name: "prepare", Duration: Duration(2 * time.Minute)},
			{Name: "execute", Duration: Duration(3 * time.Minute)},
		},
	}
}

func (generator *blueprintGenerator) paths(candidateIDs []string) []PathSpec {
	paths := make([]PathSpec, len(candidateIDs)*2)
	for index, candidateID := range candidateIDs {
		paths[index*2] = PathSpec{
			From: candidateID, To: "registry.example", Scope: "registry",
			P10Mbps: float64(100 + generator.draw("path/"+candidateID+"/registry", 1901)),
		}
		paths[index*2+1] = PathSpec{
			From: candidateID, To: "object-store.example", Scope: "public_internet",
			P10Mbps: float64(100 + generator.draw("path/"+candidateID+"/object-store", 1901)),
		}
	}
	return paths
}

func (generator *blueprintGenerator) runtimeModels(runs []RunArrivalSpec, candidateIDs []string) []RuntimeModelSpec {
	models := make([]RuntimeModelSpec, 0, len(runs)*len(candidateIDs))
	for _, run := range runs {
		for _, candidateID := range candidateIDs {
			minimum := time.Duration(30+generator.draw("runtime/"+run.Name+"/"+candidateID+"/minimum", 271)) * time.Second
			maximum := minimum + time.Duration(30+generator.draw("runtime/"+run.Name+"/"+candidateID+"/span", 271))*time.Second
			models = append(models, RuntimeModelSpec{
				Run:       run.Name,
				Candidate: candidateID,
				Minimum:   Duration(minimum),
				Maximum:   Duration(maximum),
			})
		}
	}
	return models
}

func (generator *blueprintGenerator) faults(runs []RunArrivalSpec) []FaultSpec {
	if len(runs) == 0 {
		return nil
	}
	actions := []FaultAction{FaultLoseResponse, FaultDuplicateResponse, FaultRejectCommand}
	return []FaultSpec{{
		ID: "generated-provider-fault",
		Trigger: FaultTriggerSpec{
			Operation: "provider.launch",
			Run:       runs[0].Name,
			Attempt:   1,
		},
		Action: actions[generator.draw("fault/action", uint64(len(actions)))],
	}}
}

func generatedResources(ordinal int) *ResourcesSpec {
	return &ResourcesSpec{
		CPUMillis: 4000,
		Memory:    ByteSize(16_000_000_000),
		Disk:      StatedBytes(100_000_000_000),
		GPU: &GPUSpec{
			Model:  "RTX 4090",
			Count:  1,
			Memory: ByteSize(24_000_000_000 + int64(ordinal%2)*8_000_000_000),
		},
	}
}

// generatedRunResources is what a generated Run asks for. Its disk floor is a
// share of the machine rather than all of it, because a Run reserving every byte
// a candidate has leaves nowhere for its own image, its inputs, and the caches it
// declared, and a generator that produced only such Runs would generate worlds in
// which nothing can be placed anywhere.
func generatedRunResources() *ResourcesSpec {
	resources := generatedResources(0)
	resources.Disk = StatedBytes(50_000_000_000)
	return resources
}

func generatedCandidateIDs(rentals []RentalSpec, marketplace []MarketplaceOfferSpec) []string {
	ids := make([]string, 0, len(rentals)+len(marketplace))
	for _, rental := range rentals {
		ids = append(ids, rental.ID)
	}
	for _, offer := range marketplace {
		ids = append(ids, offer.ID)
	}
	return ids
}

func (generator *blueprintGenerator) megabytes(key string, minimum, span uint64) int64 {
	return int64(minimum+generator.draw(key, span)) * 1_000_000
}

func (generator *blueprintGenerator) price(key string, minimumCents, span uint64) float64 {
	return float64(minimumCents+generator.draw(key, span)) / 100
}

func (generator *blueprintGenerator) draw(key string, modulus uint64) uint64 {
	sum := sha256.Sum256([]byte(generator.seed + "\x00" + key))
	value := binary.BigEndian.Uint64(sum[:8])
	if modulus > 0 {
		value %= modulus
	}
	generator.samples = append(generator.samples, GenerationSample{Key: key, Value: value})
	return value
}

func generatedDigest(seed, key string) string {
	sum := sha256.Sum256([]byte(seed + "\x00" + key))
	return fmt.Sprintf("%x", sum[:])
}

func shortGeneratedDigest(seed, key string) string {
	return generatedDigest(seed, key)[:12]
}
