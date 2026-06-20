package httpapi

const OpenAPIJSON = `{
  "openapi": "3.1.0",
  "info": {
    "title": "Mercator OCI Run Broker API",
    "version": "0.1.0"
  },
  "paths": {
    "/health/live": {
      "get": {
        "operationId": "healthLive",
        "responses": {"200": {"description": "live"}}
      }
    },
    "/health/ready": {
      "get": {
        "operationId": "healthReady",
        "responses": {"200": {"description": "ready"}}
      }
    },
    "/openapi.json": {
      "get": {
        "operationId": "getOpenAPI",
        "responses": {"200": {"description": "OpenAPI document"}}
      }
    },
    "/v1/runs": {
      "post": {
        "operationId": "createRun",
        "parameters": [
          {"name": "Idempotency-Key", "in": "header", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {
          "202": {"description": "Run request accepted"},
          "400": {"description": "Invalid request"}
        }
      }
    },
    "/v1/runs/{run_id}/events": {
      "get": {
        "operationId": "listRunEvents",
        "parameters": [
          {"name": "run_id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "workspace_id", "in": "query", "required": false, "schema": {"type": "string"}}
        ],
        "responses": {"200": {"description": "Run events"}}
      }
    },
    "/v1/placements:preview": {
      "post": {
        "operationId": "previewPlacement",
        "responses": {"200": {"description": "Placement decision preview"}}
      }
    }
  }
}`
