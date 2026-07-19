package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

type contractDocument struct {
	Paths map[string]map[string]struct {
		OperationID string `json:"operationId"`
		Responses   map[string]struct {
			Description string `json:"description"`
			Content     map[string]struct {
				Schema map[string]any `json:"schema"`
			} `json:"content"`
		} `json:"responses"`
	} `json:"paths"`
}

func readContract(t *testing.T) contractDocument {
	t.Helper()
	var document contractDocument
	if err := json.Unmarshal(openAPIJSON, &document); err != nil {
		t.Fatalf("decode OpenAPI contract: %v", err)
	}
	return document
}

func TestGeneratedRouterRegistersEveryContractOperation(t *testing.T) {
	router := &recordingMux{}
	HandlerWithOptions(nil, StdHTTPServerOptions{BaseRouter: router})
	var actual []string
	for _, pattern := range router.patterns {
		if isContractPattern(pattern) {
			actual = append(actual, pattern)
		}
	}
	slices.Sort(actual)

	var expected []string
	for path, operations := range readContract(t).Paths {
		for method := range operations {
			expected = append(expected, strings.ToUpper(method)+" "+path)
		}
	}
	slices.Sort(expected)

	if !slices.Equal(actual, expected) {
		t.Fatalf("registered contract routes = %v, want %v", actual, expected)
	}
}

type recordingMux struct {
	patterns []string
}

func (m *recordingMux) HandleFunc(pattern string, _ func(http.ResponseWriter, *http.Request)) {
	m.patterns = append(m.patterns, pattern)
}

func (*recordingMux) ServeHTTP(http.ResponseWriter, *http.Request) {}

func TestEveryResponseHasAJSONSchema(t *testing.T) {
	for path, operations := range readContract(t).Paths {
		for method, operation := range operations {
			for status, response := range operation.Responses {
				mediaType, ok := response.Content["application/json"]
				if !ok || len(mediaType.Schema) == 0 {
					t.Errorf("%s %s response %s has no application/json schema", strings.ToUpper(method), path, status)
				}
			}
		}
	}
}

func TestPublicObjectSchemasAreConcrete(t *testing.T) {
	var document any
	if err := json.Unmarshal(openAPIJSON, &document); err != nil {
		t.Fatalf("decode OpenAPI contract: %v", err)
	}
	assertConcreteObjectSchemas(t, "openapi", document)
}

func assertConcreteObjectSchemas(t *testing.T, path string, value any) {
	t.Helper()
	switch value := value.(type) {
	case []any:
		for index, child := range value {
			assertConcreteObjectSchemas(t, fmt.Sprintf("%s[%d]", path, index), child)
		}
	case map[string]any:
		if value["type"] == "object" && value["properties"] == nil && value["additionalProperties"] == nil {
			t.Errorf("%s is an opaque object schema", path)
		}
		for name, child := range value {
			assertConcreteObjectSchemas(t, path+"."+name, child)
		}
	}
}

func isContractPattern(pattern string) bool {
	_, path, found := strings.Cut(pattern, " ")
	return found && (strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/health/") || path == "/openapi.json")
}
