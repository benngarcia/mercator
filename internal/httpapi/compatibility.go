package httpapi

import "runtime/debug"

const (
	storageEpoch = "single-scope-v1"
	apiEpoch     = "single-scope-v2"
)

var supportedClientEpochs = []string{"workspace-client-v1", "single-scope-client-v2"}
var compatibilityFeatures = []string{"legacy_workspace_selectors", "singular_decision"}
var buildRevisionOverride string

func buildRevision() string {
	if buildRevisionOverride != "" {
		return buildRevisionOverride
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "development"
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value
		}
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "development"
}
