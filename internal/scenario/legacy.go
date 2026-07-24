package scenario

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
)

func adaptLegacyBlueprint(data []byte) ([]byte, error) {
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	status, ok := document["status"].(string)
	if !ok || status == "" {
		return nil, fmt.Errorf("status is required")
	}
	delete(document, "status")
	document["schema"] = string(BlueprintSchemaV1)
	document["classification"] = status
	adaptLegacyCapabilities(document)

	world := object(document["world"])
	layerDigests, imageRefs := adaptLegacyImages(world)
	artifacts, err := newLegacyArtifacts(world)
	if err != nil {
		return nil, err
	}
	if err := adaptLegacyRentals(world, artifacts, layerDigests, imageRefs); err != nil {
		return nil, err
	}
	if err := adaptLegacyRequest(object(document["request"]), artifacts, imageRefs); err != nil {
		return nil, err
	}
	adaptLegacyExpectation(object(document["expect"]))
	if err := adaptLegacyTimeline(array(document["timeline"]), artifacts, imageRefs); err != nil {
		return nil, err
	}
	if len(artifacts.order) > 0 {
		world["artifacts"] = artifacts.documents()
	}

	return json.Marshal(document)
}

func adaptLegacyCapabilities(document map[string]any) {
	values := array(document["missing_capabilities"])
	if len(values) == 0 {
		return
	}
	adapted := make([]any, 0, len(values))
	for _, value := range values {
		switch value {
		case "cache_mounts":
			value = "artifacts"
		case "cache_evidence":
			value = "artifact_evidence"
		}
		if !slices.Contains(adapted, value) {
			adapted = append(adapted, value)
		}
	}
	document["missing_capabilities"] = adapted
}

type legacyArtifacts struct {
	order []string
	byID  map[string]map[string]any
}

func newLegacyArtifacts(world map[string]any) (*legacyArtifacts, error) {
	artifacts := &legacyArtifacts{byID: map[string]map[string]any{}}
	for _, value := range array(world["artifacts"]) {
		document := object(value)
		if err := artifacts.add(document["id"], document["size"]); err != nil {
			return nil, err
		}
	}
	return artifacts, nil
}

func (a *legacyArtifacts) add(rawID, size any) error {
	id, ok := rawID.(string)
	if !ok || id == "" {
		return fmt.Errorf("legacy Artifact identity is empty")
	}
	if existing := a.byID[id]; existing != nil {
		if !reflect.DeepEqual(existing["size"], size) {
			return fmt.Errorf("legacy Artifact %q has conflicting sizes", id)
		}
		return nil
	}
	a.order = append(a.order, id)
	a.byID[id] = map[string]any{"id": id, "size": size}
	return nil
}

func (a *legacyArtifacts) documents() []any {
	documents := make([]any, 0, len(a.order))
	for _, id := range a.order {
		documents = append(documents, a.byID[id])
	}
	return documents
}

func adaptLegacyImages(world map[string]any) (map[string]string, map[string]string) {
	layerDigests := map[string]string{}
	imageRefs := map[string]string{}
	images := object(world["images"])
	canonical := make(map[string]any, len(images))
	refs := make([]string, 0, len(images))
	for ref := range images {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		value := images[ref]
		canonicalRef := legacyImageRef(ref)
		imageRefs[ref] = canonicalRef
		canonical[canonicalRef] = value
		for _, rawLayer := range array(object(value)["layers"]) {
			layer := object(rawLayer)
			name, _ := layer["name"].(string)
			if name == "" {
				continue
			}
			digest := legacyLayerDigest(name)
			layerDigests[name] = digest
			delete(layer, "name")
			layer["digest"] = digest
		}
	}
	if len(images) > 0 {
		world["images"] = canonical
	}
	return layerDigests, imageRefs
}

func legacyLayerDigest(name string) string {
	if ociDigestPattern.MatchString(name) {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return fmt.Sprintf("sha256:%x", sum)
}

func legacyImageRef(ref string) string {
	if ociImageRefPattern.MatchString(ref) {
		return ref
	}
	sum := sha256.Sum256([]byte(ref))
	return fmt.Sprintf("sha256:%x", sum)
}

func adaptLegacyRentals(
	world map[string]any,
	artifacts *legacyArtifacts,
	layerDigests map[string]string,
	imageRefs map[string]string,
) error {
	for _, value := range array(world["rentals"]) {
		rental := object(value)
		cachedImages := array(rental["cached_images"])
		for index, rawRef := range cachedImages {
			ref, _ := rawRef.(string)
			if canonical := imageRefs[ref]; canonical != "" {
				cachedImages[index] = canonical
			}
		}
		cachedLayers := array(rental["cached_layers"])
		for index, rawName := range cachedLayers {
			name, _ := rawName.(string)
			if digest := layerDigests[name]; digest != "" {
				cachedLayers[index] = digest
			}
		}
		namedCaches := object(rental["named_caches"])
		if len(namedCaches) == 0 {
			continue
		}
		ids := make([]string, 0, len(namedCaches))
		for id := range namedCaches {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if err := artifacts.add(id, namedCaches[id]); err != nil {
				return err
			}
		}
		rental["artifact_replicas"] = stringsToAny(ids)
		delete(rental, "named_caches")
	}
	return nil
}

func adaptLegacyTimeline(steps []any, artifacts *legacyArtifacts, imageRefs map[string]string) error {
	for _, value := range steps {
		step := object(value)
		if err := adaptLegacyRequest(object(step["request"]), artifacts, imageRefs); err != nil {
			return err
		}
		adaptLegacyExpectation(object(step["expect"]))
	}
	return nil
}

func adaptLegacyRequest(request map[string]any, artifacts *legacyArtifacts, imageRefs map[string]string) error {
	if ref, _ := request["image"].(string); imageRefs[ref] != "" {
		request["image"] = imageRefs[ref]
	}
	mounts := array(request["cache_mounts"])
	if len(mounts) == 0 {
		return nil
	}
	consumes := make([]any, 0, len(mounts))
	for _, value := range mounts {
		mount := object(value)
		if err := artifacts.add(mount["key"], mount["size"]); err != nil {
			return err
		}
		consumes = append(consumes, mount["key"])
	}
	request["consumes_artifacts"] = consumes
	delete(request, "cache_mounts")
	return nil
}

func adaptLegacyExpectation(expectation map[string]any) {
	for _, value := range object(expectation["candidates"]) {
		candidate := object(value)
		if caches := candidate["caches"]; caches != nil {
			candidate["artifact_evidence"] = caches
			delete(candidate, "caches")
		}
	}
}

func object(value any) map[string]any {
	document, _ := value.(map[string]any)
	return document
}

func array(value any) []any {
	values, _ := value.([]any)
	return values
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}
