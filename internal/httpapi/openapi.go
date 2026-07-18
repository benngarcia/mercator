package httpapi

import _ "embed"

//go:generate go tool oapi-codegen --config oapi-codegen.yaml openapi.json

//go:embed openapi.json
var openAPIJSON []byte

var OpenAPIJSON = string(openAPIJSON)
