package httpapi

import (
	_ "embed"
	"encoding/json"
)

//go:generate go tool oapi-codegen --config oapi-codegen.yaml openapi.json

//go:embed openapi.json
var openAPIJSON []byte

var OpenAPIJSON = string(openAPIJSON)

var openAPIDocument = func() map[string]interface{} {
	var document map[string]interface{}
	if err := json.Unmarshal(openAPIJSON, &document); err != nil {
		panic(err)
	}
	return document
}()
