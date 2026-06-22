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
          "202": {"description": "Run request accepted", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/RunResponse"}}}},
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
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/PlacementPreviewRequest"}}}},
        "responses": {
          "200": {"description": "Placement decision preview", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/PlacementPreviewResponse"}}}},
          "400": {"description": "Invalid request", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}},
          "401": {"description": "Unauthorized", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}},
          "403": {"description": "Forbidden", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}},
          "502": {"description": "Offer query failed", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}}
        }
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
      "CreateRunRequest": {"type": "object", "description": "Create a run. The only required input is an image (top-level shorthand) or a full workload spec. run_id is optional and server-generated (uuidv7) when omitted; an Idempotency-Key header is required for retry-safe replay.", "properties": {"workspace_id": {"type": "string"}, "run_id": {"type": "string", "description": "Optional. When omitted the server generates a uuidv7-based run id and returns it."}, "workload_id": {"type": "string"}, "workload_revision_id": {"type": "string"}, "image": {"type": "string", "description": "Top-level image shorthand. Synthesizes the single container when no full workload spec is supplied. Ignored when an explicit workload spec is present."}, "args": {"type": "array", "items": {"type": "string"}, "description": "Container args for the image shorthand."}, "env": {"type": "object", "description": "Run-level literal env bindings. For stored or explicit workload specs, these override or add to the workload container env for this run only. For image shorthand, these become the synthesized container env.", "additionalProperties": {"$ref": "#/components/schemas/EnvBinding"}}, "workload": {"type": "object", "description": "Full workload revision spec. Takes precedence over the image shorthand when both are supplied."}}},
      "Run": {"type": "object", "required": ["id", "workspace_id", "phase", "cleanup", "closed"], "properties": {"id": {"type": "string"}, "workspace_id": {"type": "string"}, "workload_revision_id": {"type": "string"}, "phase": {"type": "string"}, "outcome": {"type": "string", "enum": ["succeeded", "failed", "cancelled"]}, "exit_code": {"type": "integer", "description": "Container exit code, surfaced once observed. Absent until a terminal observation is recorded."}, "cleanup": {"type": "string", "enum": ["not_required", "pending", "confirmed", "blocked"]}, "disposition": {"type": "string", "enum": ["release", "terminate"], "description": "Recorded cleanup disposition. terminate: the run provisioned a host we own that is destroyed on cleanup. release: the run borrowed a slot in a standing pool we do not own; cleanup removes only our job. Recorded at launch time and dispatched on the recorded value, never re-inferred at cleanup time. Absent until a launch intent is recorded."}, "closed": {"type": "boolean"}}},
      "CreateWorkloadRequest": {"type": "object", "required": ["workspace_id", "workload_id", "name"], "properties": {"workspace_id": {"type": "string"}, "workload_id": {"type": "string"}, "name": {"type": "string"}}},
      "CreateRevisionRequest": {"type": "object", "required": ["revision"], "properties": {"revision": {"type": "object"}}},
      "WorkloadRevisionResponse": {"type": "object", "required": ["revision"], "properties": {"revision": {"type": "object"}}},
      "WorkloadRevisionListResponse": {"type": "object", "required": ["revisions"], "properties": {"revisions": {"type": "array", "items": {"type": "object"}}}},
      "ResolveImageRequest": {"type": "object", "required": ["image", "platform"], "properties": {"image": {"type": "string"}, "platform": {"type": "string"}}},
      "ResolveImageResponse": {"type": "object", "required": ["image"], "properties": {"image": {"type": "object"}}},
      "EnvBinding": {"type": "object", "required": ["value"], "properties": {"value": {"type": "string"}}},
      "RunResponse": {"type": "object", "required": ["run_id", "run"], "properties": {"run_id": {"type": "string", "description": "Convenience top-level run identifier, equal to run.id. Returned on every run response alongside the full run record."}, "run": {"$ref": "#/components/schemas/Run"}, "metadata": {"type": "object", "description": "Reserved for per-response metadata.", "additionalProperties": true}, "links": {"type": "object", "additionalProperties": {"type": "string"}}, "duplicate": {"type": "boolean", "description": "True when this create was a safe idempotent replay of an existing run."}}},
      "RunListResponse": {"type": "object", "required": ["runs"], "properties": {"runs": {"type": "array", "items": {"$ref": "#/components/schemas/Run"}}}},
      "EventListResponse": {"type": "object", "required": ["events"], "properties": {"events": {"type": "array", "items": {"type": "object"}}}},
      "PlacementPreviewRequest": {"type": "object", "required": ["workload"], "properties": {"run_id": {"type": "string"}, "workspace_id": {"type": "string"}, "workload": {"type": "object"}}},
      "PlacementPreviewResponse": {"type": "object", "required": ["decision"], "properties": {"decision": {"type": "object"}}},
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
