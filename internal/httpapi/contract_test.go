package httpapi

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type contractDocument struct {
	Paths map[string]map[string]struct {
		OperationID string `json:"operationId"`
		Responses   map[string]struct {
			Description string `json:"description"`
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
	server, ok := newHTTPTestServer(t).(*Server)
	if !ok {
		t.Fatal("HTTP handler is not *Server")
	}
	var actual []string
	for _, pattern := range server.mux.patterns {
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

func isContractPattern(pattern string) bool {
	_, path, found := strings.Cut(pattern, " ")
	return found && (strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/health/") || path == "/openapi.json")
}

func TestHandlersOnlyWriteDeclaredResponseStatuses(t *testing.T) {
	document := readContract(t)
	functions := parseHTTPFunctions(t)
	for _, operations := range document.Paths {
		for _, operation := range operations {
			method := operationMethodName(operation.OperationID)
			if functions[method] == nil {
				t.Errorf("%s has no %s handler", operation.OperationID, method)
				continue
			}
			written := statusesWrittenBy(functions, operation.OperationID)
			for status := range written {
				code := strconv.Itoa(status)
				if _, declared := operation.Responses[code]; !declared {
					t.Errorf("%s writes undeclared HTTP %s", operation.OperationID, code)
				}
			}
			for code, response := range operation.Responses {
				status, err := strconv.Atoi(code)
				if err == nil && strings.HasPrefix(response.Description, "HTTP ") {
					if _, observed := written[status]; !observed {
						t.Errorf("%s declares unused handler-derived HTTP %s", operation.OperationID, code)
					}
				}
			}
		}
	}
}

func parseHTTPFunctions(t *testing.T) map[string]*ast.FuncDecl {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read httpapi package: %v", err)
	}
	functions := map[string]*ast.FuncDecl{}
	files := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") || strings.HasSuffix(entry.Name(), ".gen.go") {
			continue
		}
		path := filepath.Clean(entry.Name())
		file, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Body != nil {
				functions[function.Name.Name] = function
			}
		}
	}
	return functions
}

func statusesWrittenBy(functions map[string]*ast.FuncDecl, operationID string) map[int]struct{} {
	statuses := map[int]struct{}{}
	visited := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		function := functions[name]
		if function == nil {
			return
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if status, ok := responseStatus(call); ok {
				statuses[status] = struct{}{}
			}
			switch called := call.Fun.(type) {
			case *ast.Ident:
				visit(called.Name)
			case *ast.SelectorExpr:
				if receiver, ok := called.X.(*ast.Ident); ok && receiver.Name == "s" {
					visit(called.Sel.Name)
				}
			}
			return true
		})
	}
	visit(operationMethodName(operationID))
	return statuses
}

func operationMethodName(operationID string) string {
	parts := strings.FieldsFunc(operationID, func(r rune) bool { return r == '_' || r == '-' })
	if len(parts) == 1 {
		parts = splitCamelCase(operationID)
	}
	for index, part := range parts {
		switch strings.ToLower(part) {
		case "api":
			parts[index] = "API"
		default:
			parts[index] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "")
}

func splitCamelCase(value string) []string {
	var parts []string
	start := 0
	for index, character := range value {
		if index > 0 && character >= 'A' && character <= 'Z' {
			parts = append(parts, value[start:index])
			start = index
		}
	}
	return append(parts, value[start:])
}

func responseStatus(call *ast.CallExpr) (int, bool) {
	name, ok := call.Fun.(*ast.Ident)
	if !ok || len(call.Args) < 2 || (name.Name != "writeJSON" && name.Name != "writeError" && name.Name != "writeInternalError") {
		return 0, false
	}
	selector, ok := call.Args[1].(*ast.SelectorExpr)
	if !ok {
		return 0, false
	}
	status, ok := httpStatusCodes[selector.Sel.Name]
	return status, ok
}

var httpStatusCodes = map[string]int{
	"StatusOK":                    http.StatusOK,
	"StatusCreated":               http.StatusCreated,
	"StatusAccepted":              http.StatusAccepted,
	"StatusBadRequest":            http.StatusBadRequest,
	"StatusUnauthorized":          http.StatusUnauthorized,
	"StatusForbidden":             http.StatusForbidden,
	"StatusNotFound":              http.StatusNotFound,
	"StatusRequestTimeout":        http.StatusRequestTimeout,
	"StatusConflict":              http.StatusConflict,
	"StatusRequestEntityTooLarge": http.StatusRequestEntityTooLarge,
	"StatusInternalServerError":   http.StatusInternalServerError,
	"StatusNotImplemented":        http.StatusNotImplemented,
	"StatusBadGateway":            http.StatusBadGateway,
}
