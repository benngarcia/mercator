// The OpenAPI document owns every public transport shape. This module gives
// the console stable domain names without replacing generated fields.

import type { components, operations } from "./contract.gen";
import * as Schema from "effect/Schema";

type ContractSchemas = components["schemas"];

export type Platform = ContractSchemas["Platform"];
export type PortSpec = ContractSchemas["PortSpec"];
export type PortExposure = PortSpec["exposure"];
export type ContainerSpec = ContractSchemas["ContainerSpec"];
export type EnvBinding = ContractSchemas["EnvBinding"];
export type CPURequirement = ContractSchemas["CPURequirement"];
export type MemoryRequirement = ContractSchemas["MemoryRequirement"];
export type DiskRequirement = ContractSchemas["DiskRequirement"];
export type AcceleratorRequirement = ContractSchemas["AcceleratorRequirement"];
export type ResourceRequirements = ContractSchemas["ResourceRequirements"];
export type NetworkDownloadRequirement =
  ContractSchemas["NetworkDownloadRequirement"];
export type NetworkScope = NetworkDownloadRequirement["scope"];
export type NetworkRequirements = ContractSchemas["NetworkRequirements"];
export type InboundNetworkMode = NetworkRequirements["inbound"];
export type PlacementPolicy = ContractSchemas["PlacementPolicy"];
export type PlacementObjective = PlacementPolicy["objective"];
export type ExecutionPolicy = ContractSchemas["ExecutionPolicy"];
export type WorkloadSpec = ContractSchemas["WorkloadSpec"];
export type WorkloadRevision = ContractSchemas["WorkloadRevision"];

export type AcceleratorInventory = ContractSchemas["AcceleratorInventory"];
export type ResourceInventory = ContractSchemas["ResourceInventory"];
export type ContainerCapabilities = ContractSchemas["ContainerCapabilities"];
export type LifecycleCapabilities = ContractSchemas["LifecycleCapabilities"];
export type ResourceCapabilities = ContractSchemas["ResourceCapabilities"];
export type NetworkCapabilities = ContractSchemas["NetworkCapabilities"];
export type PricingCapabilities = ContractSchemas["PricingCapabilities"];
export type ObservabilityCapabilities =
  ContractSchemas["ObservabilityCapabilities"];
export type CapabilityProfile = ContractSchemas["CapabilityProfile"];
export type NetworkFact = ContractSchemas["NetworkFact"];
export type NetworkFacts = ContractSchemas["NetworkFacts"];
export type PriceModel = ContractSchemas["PriceModel"];
export type QueueSnapshot = ContractSchemas["QueueSnapshot"];
export type Estimate = ContractSchemas["Estimate"];
export type ImageCacheEvidence = ContractSchemas["ImageCacheEvidence"];
export type CapacityEvidence = ContractSchemas["CapacityEvidence"];
export type ReliabilityEvidence = ContractSchemas["ReliabilityEvidence"];
export type OfferSnapshot = ContractSchemas["OfferSnapshot"];
export type OfferKind = OfferSnapshot["kind"];

export type Violation = ContractSchemas["Violation"];
export type CandidateEstimates = ContractSchemas["CandidateEstimates"];
export type CandidateDecision = ContractSchemas["CandidateDecision"];
export type CollectionReport = ContractSchemas["CollectionReport"];
export type BookingDecision = ContractSchemas["BookingDecision"];
export type Booking = ContractSchemas["Booking"];

export type RunRecord = ContractSchemas["Run"];
export type Run = RunRecord;
export type RunOutcome = NonNullable<RunRecord["outcome"]>;
export type CleanupState = RunRecord["cleanup"];
export type Disposition = NonNullable<RunRecord["disposition"]>;

// The server stores phase as a forward-compatible string. The console renders
// the lifecycle phases it understands through this narrower view model.
export type RunPhase =
  | "requested"
  | "launching"
  | "running"
  | "cleaning_up"
  | "closed";

export type CloudEvent = ContractSchemas["CloudEvent"];
export type CredentialRef = ContractSchemas["Credential"];
export type CredentialSource = CredentialRef["source"];
export type ConnectionRecord = ContractSchemas["ConnectionRecord"];
export type ResolvedImage = ContractSchemas["ResolvedImage"];
export type Workspace = ContractSchemas["Workspace"];
export type CreateWorkspaceRequest = ContractSchemas["CreateWorkspaceRequest"];
export type WorkspaceResponse = ContractSchemas["WorkspaceResponse"];
export type WorkspaceListResponse = ContractSchemas["WorkspaceListResponse"];

// /auth/session belongs to the browser login surface rather than the public
// versioned HTTP contract. Decode its deliberately small response where it
// enters the console instead of pretending it is part of the /v1 contract.
export const AuthSessionState = Schema.Struct({
  enabled: Schema.Boolean,
  email: Schema.optionalKey(Schema.String),
});

export interface AuthSessionState extends Schema.Schema.Type<
  typeof AuthSessionState
> {}

export type AdapterManifest = ContractSchemas["AdapterManifest"];
export type ConfigFieldType = AdapterManifest["config_fields"][number]["type"];
export type AdapterConfigField = AdapterManifest["config_fields"][number];
export type AdapterCredentialSpec = AdapterManifest["credential"];
export type AdapterSetupStep = AdapterManifest["setup_steps"][number];

export type CreateConnectionRequest =
  ContractSchemas["CreateConnectionRequest"];
export type ConnectionResponse = ContractSchemas["ConnectionResponse"];
export type ConnectionListResponse = ContractSchemas["ConnectionListResponse"];
export type DeleteConnectionResponse =
  operations["deleteConnection"]["responses"][200]["content"]["application/json"];
export type AdapterListResponse = ContractSchemas["AdapterListResponse"];

export type SinkStatus = ContractSchemas["SinkStatus"];
export type SinkResult = ContractSchemas["SinkResult"];
export type ReplaySinkRequest = ContractSchemas["ReplaySinkRequest"];

export type ResolveImageRequest = ContractSchemas["ResolveImageRequest"];
export type ResolveImageResponse = ContractSchemas["ResolveImageResponse"];
export type CreateRunRequest = ContractSchemas["CreateRunRequest"];
export type CreateWorkloadRequest = ContractSchemas["CreateWorkloadRequest"];
export type CreateRevisionRequest = ContractSchemas["CreateRevisionRequest"];
export type PlacementPreviewRequest =
  ContractSchemas["PlacementPreviewRequest"];

export type RunResponse = ContractSchemas["RunResponse"];
export type RunListResponse = ContractSchemas["RunListResponse"];
export type EventListResponse = ContractSchemas["EventListResponse"];
export type BookingDecisionResponse =
  ContractSchemas["BookingDecisionResponse"];
export type PlacementPreviewResponse =
  ContractSchemas["PlacementPreviewResponse"];
export type OfferListResponse = ContractSchemas["OfferListResponse"];
export type WorkloadRevisionResponse =
  ContractSchemas["WorkloadRevisionResponse"];
export type WorkloadRevisionListResponse =
  ContractSchemas["WorkloadRevisionListResponse"];
export type CreateWorkloadResponse =
  operations["createWorkload"]["responses"][202]["content"]["application/json"];

export type ErrorEnvelope = ContractSchemas["ErrorResponse"];
