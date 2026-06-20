package httpapi

const OpenAPIJSON = `{
  "openapi": "3.1.0",
  "info": {
    "title": "Mercator OCI Run Broker API",
    "version": "0.1.0"
  },
  "security": [{"bearerAuth": []}],
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
      "get": {
        "operationId": "listRuns",
        "parameters": [
          {"name": "workspace_id", "in": "query", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {
          "200": {"description": "Run list", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/RunListResponse"}}}},
          "400": {"description": "Invalid request", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}}
        }
      },
      "post": {
        "operationId": "createRun",
        "parameters": [
          {"name": "Idempotency-Key", "in": "header", "required": true, "schema": {"type": "string"}}
        ],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/CreateRunRequest"}}}},
        "responses": {
          "202": {"description": "Run request accepted", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/CreateRunResponse"}}}},
          "400": {"description": "Invalid request", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}},
          "409": {"description": "IdempotencyConflict", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}}
        }
      }
    },
    "/v1/runs/{run_id}": {
      "get": {
        "operationId": "getRun",
        "parameters": [
          {"name": "run_id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "workspace_id", "in": "query", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {
          "200": {"description": "Run", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/RunResponse"}}}},
          "404": {"description": "Run not found", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}}
        }
      }
    },
    "/v1/runs/{run_id}:wait": {
      "get": {
        "operationId": "waitRun",
        "parameters": [
          {"name": "run_id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "workspace_id", "in": "query", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {"200": {"description": "Run", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/RunResponse"}}}}}
      }
    },
    "/v1/runs/{run_id}:refresh": {
      "post": {
        "operationId": "refreshRun",
        "parameters": [
          {"name": "run_id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "workspace_id", "in": "query", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {"200": {"description": "Run", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/RunResponse"}}}}}
      }
    },
    "/v1/runs/{run_id}:cancel": {
      "post": {
        "operationId": "cancelRun",
        "parameters": [
          {"name": "run_id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "workspace_id", "in": "query", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {"200": {"description": "Run", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/RunResponse"}}}}}
      }
    },
    "/v1/runs/{run_id}/events": {
      "get": {
        "operationId": "listRunEvents",
        "parameters": [
          {"name": "run_id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "workspace_id", "in": "query", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {"200": {"description": "Run events", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/EventListResponse"}}}}}
      }
    },
    "/v1/runs/{run_id}/decision": {
      "get": {
        "operationId": "getRunDecision",
        "parameters": [
          {"name": "run_id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "workspace_id", "in": "query", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {"200": {"description": "Placement decision", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/PlacementDecisionResponse"}}}}}
      }
    },
    "/v1/placements:preview": {
      "post": {
        "operationId": "previewPlacement",
        "responses": {"200": {"description": "Placement decision preview"}}
      }
    },
    "/v1/connections": {
      "get": {
        "operationId": "listConnections",
        "parameters": [
          {"name": "workspace_id", "in": "query", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {
          "200": {"description": "Connection list", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ConnectionListResponse"}}}},
          "400": {"description": "Invalid request", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}}
        }
      }
    },
    "/v1/offers": {
      "get": {
        "operationId": "listOffers",
        "parameters": [
          {"name": "workspace_id", "in": "query", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {
          "200": {"description": "Offer list", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/OfferListResponse"}}}},
          "400": {"description": "Invalid request", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}}
        }
      }
    },
    "/v1/workloads": {
      "post": {
        "operationId": "createWorkload",
        "parameters": [
          {"name": "Idempotency-Key", "in": "header", "required": true, "schema": {"type": "string"}}
        ],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/CreateWorkloadRequest"}}}},
        "responses": {
          "202": {"description": "Workload created"},
          "400": {"description": "Invalid request", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}}
        }
      }
    },
    "/v1/workloads/{workload_id}/revisions": {
      "get": {
        "operationId": "listWorkloadRevisions",
        "parameters": [
          {"name": "workload_id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "workspace_id", "in": "query", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {"200": {"description": "Workload revisions", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/WorkloadRevisionListResponse"}}}}}
      },
      "post": {
        "operationId": "createWorkloadRevision",
        "parameters": [
          {"name": "workload_id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "workspace_id", "in": "query", "required": true, "schema": {"type": "string"}},
          {"name": "Idempotency-Key", "in": "header", "required": true, "schema": {"type": "string"}}
        ],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/CreateRevisionRequest"}}}},
        "responses": {"202": {"description": "Workload revision created", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/WorkloadRevisionResponse"}}}}}
      }
    },
    "/v1/workloads/{workload_id}/revisions/{revision_id}": {
      "get": {
        "operationId": "getWorkloadRevision",
        "parameters": [
          {"name": "workload_id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "revision_id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "workspace_id", "in": "query", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {"200": {"description": "Workload revision", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/WorkloadRevisionResponse"}}}}}
      }
    },
    "/v1/images:resolve": {
      "post": {
        "operationId": "resolveImage",
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ResolveImageRequest"}}}},
        "responses": {"200": {"description": "Resolved image", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ResolveImageResponse"}}}}}
      }
    },
    "/v1/secrets": {
      "get": {
        "operationId": "listSecrets",
        "parameters": [
          {"name": "workspace_id", "in": "query", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {"200": {"description": "Secret metadata", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SecretMetadataListResponse"}}}}}
      }
    },
    "/v1/secrets/{secret_id}/versions": {
      "post": {
        "operationId": "createSecretVersion",
        "parameters": [
          {"name": "secret_id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "Idempotency-Key", "in": "header", "required": true, "schema": {"type": "string"}}
        ],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/CreateSecretVersionRequest"}}}},
        "responses": {"202": {"description": "Secret version metadata", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/CreateSecretVersionResponse"}}}}}
      }
    },
    "/v1/secrets/{secret_id}/grants": {
      "post": {
        "operationId": "grantSecret",
        "parameters": [
          {"name": "secret_id", "in": "path", "required": true, "schema": {"type": "string"}}
        ],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/GrantSecretRequest"}}}},
        "responses": {"202": {"description": "Secret grant", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SecretGrantResponse"}}}}}
      }
    },
    "/v1/sinks/{sink_id}": {
      "get": {
        "operationId": "getSinkStatus",
        "parameters": [
          {"name": "sink_id", "in": "path", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {
          "200": {"description": "Sink status", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SinkStatus"}}}},
          "404": {"description": "Sink not found", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}}
        }
      }
    },
    "/v1/sinks/{sink_id}:deliver": {
      "post": {
        "operationId": "deliverSink",
        "parameters": [
          {"name": "sink_id", "in": "path", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {
          "202": {"description": "Sink delivery result", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SinkResult"}}}},
          "502": {"description": "Sink delivery failed", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}}
        }
      }
    },
    "/v1/sinks/{sink_id}:replay": {
      "post": {
        "operationId": "replaySink",
        "parameters": [
          {"name": "sink_id", "in": "path", "required": true, "schema": {"type": "string"}}
        ],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ReplaySinkRequest"}}}},
        "responses": {
          "202": {"description": "Sink replay result", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SinkResult"}}}},
          "502": {"description": "Sink replay failed", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}}
        }
      }
    }
  },
  "components": {
    "securitySchemes": {
      "bearerAuth": {"type": "http", "scheme": "bearer"}
    },
    "schemas": {
      "CreateRunRequest": {"type": "object", "required": ["run_id"], "properties": {"workspace_id": {"type": "string"}, "run_id": {"type": "string"}, "workload_id": {"type": "string"}, "workload_revision_id": {"type": "string"}, "workload": {"type": "object"}}},
      "CreateRunResponse": {"type": "object", "required": ["run_id"], "properties": {"run_id": {"type": "string"}, "duplicate": {"type": "boolean"}}},
      "CreateWorkloadRequest": {"type": "object", "required": ["workspace_id", "workload_id", "name"], "properties": {"workspace_id": {"type": "string"}, "workload_id": {"type": "string"}, "name": {"type": "string"}}},
      "CreateRevisionRequest": {"type": "object", "required": ["revision"], "properties": {"revision": {"type": "object"}}},
      "WorkloadRevisionResponse": {"type": "object", "required": ["revision"], "properties": {"revision": {"type": "object"}}},
      "WorkloadRevisionListResponse": {"type": "object", "required": ["revisions"], "properties": {"revisions": {"type": "array", "items": {"type": "object"}}}},
      "ResolveImageRequest": {"type": "object", "required": ["image", "platform"], "properties": {"image": {"type": "string"}, "platform": {"type": "string"}}},
      "ResolveImageResponse": {"type": "object", "required": ["image"], "properties": {"image": {"type": "object"}}},
      "CreateSecretVersionRequest": {"type": "object", "required": ["workspace_id", "value"], "properties": {"workspace_id": {"type": "string"}, "secret_id": {"type": "string"}, "value": {"type": "string", "writeOnly": true}}},
      "CreateSecretVersionResponse": {"type": "object", "required": ["secret_id", "version"], "properties": {"secret_id": {"type": "string"}, "version": {"type": "integer"}}},
      "SecretMetadataListResponse": {"type": "object", "required": ["secrets"], "properties": {"secrets": {"type": "array", "items": {"type": "object", "required": ["secret_id", "version"], "properties": {"secret_id": {"type": "string"}, "version": {"type": "integer"}}}}}},
      "GrantSecretRequest": {"type": "object", "required": ["workspace_id", "version", "scope_type", "scope_id"], "properties": {"workspace_id": {"type": "string"}, "secret_id": {"type": "string"}, "version": {"type": "integer"}, "scope_type": {"type": "string"}, "scope_id": {"type": "string"}}},
      "SecretGrantResponse": {"type": "object", "required": ["grant"], "properties": {"grant": {"type": "object"}}},
      "RunResponse": {"type": "object", "required": ["run"], "properties": {"run": {"type": "object"}, "links": {"type": "object", "additionalProperties": {"type": "string"}}}},
      "RunListResponse": {"type": "object", "required": ["runs"], "properties": {"runs": {"type": "array", "items": {"type": "object"}}}},
      "EventListResponse": {"type": "object", "required": ["events"], "properties": {"events": {"type": "array", "items": {"type": "object"}}}},
      "PlacementDecisionResponse": {"type": "object", "required": ["decision"], "properties": {"decision": {"type": "object"}}},
      "ConnectionListResponse": {"type": "object", "required": ["connections"], "properties": {"connections": {"type": "array", "items": {"type": "object"}}}},
      "OfferListResponse": {"type": "object", "required": ["offers"], "properties": {"offers": {"type": "array", "items": {"type": "object"}}}},
      "ReplaySinkRequest": {"type": "object", "properties": {"from_exclusive": {"type": "integer", "minimum": 0}, "limit": {"type": "integer", "minimum": 1}, "replay_id": {"type": "string"}}},
      "SinkResult": {"type": "object", "required": ["sink_id", "delivered", "last_position"], "properties": {"sink_id": {"type": "string"}, "delivered": {"type": "integer"}, "last_position": {"type": "integer"}, "failed_event_id": {"type": "string"}, "replay_id": {"type": "string"}}},
      "SinkStatus": {"type": "object", "required": ["sink_id", "cursor", "has_cursor"], "properties": {"sink_id": {"type": "string"}, "cursor": {"type": "integer"}, "has_cursor": {"type": "boolean"}}},
      "ErrorResponse": {"type": "object", "required": ["code", "message"], "properties": {"code": {"type": "string"}, "message": {"type": "string"}}}
    }
  }
}`
