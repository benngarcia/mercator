package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/httpapi"
)

type trialClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func (client trialClient) createWorkspace(ctx context.Context, displayName string) (string, error) {
	request := httpapi.CreateWorkspaceRequest{DisplayName: displayName}
	var response httpapi.WorkspaceResponse
	if err := client.do(ctx, http.MethodPost, "/v1/workspaces", "", request, &response); err != nil {
		return "", fmt.Errorf("create trial workspace: %w", err)
	}
	return response.Workspace.ID, nil
}

func (client trialClient) ready(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var response map[string]string
		if err := client.do(ctx, http.MethodGet, "/health/ready", "", nil, &response); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (client trialClient) createAndAuthorizeConnection(ctx context.Context, identity trialIdentity, trial Trial) error {
	request := httpapi.CreateConnectionRequest{WorkspaceId: identity.workspaceID, ConnectionId: identity.connectionID, AdapterType: trial.AdapterType, Config: trial.Config}
	if trial.CredentialEnv != "" {
		request.Credential = credential.Credential{Source: credential.SourceEnv, Ref: trial.CredentialEnv}
	}
	if err := client.do(ctx, http.MethodPost, "/v1/connections", "connection:create:"+identity.connectionID, request, &httpapi.ConnectionResponse{}); err != nil {
		return fmt.Errorf("create trial connection: %w", err)
	}
	path := "/v1/connections/" + url.PathEscape(identity.connectionID) + "/authorize?workspace_id=" + url.QueryEscape(identity.workspaceID)
	if err := client.do(ctx, http.MethodPost, path, "", nil, &httpapi.ConnectionResponse{}); err != nil {
		return fmt.Errorf("authorize trial connection: %w", err)
	}
	return nil
}

func (client trialClient) affordableOffer(ctx context.Context, workspaceID string, trial Trial) (domain.OfferSnapshot, *TrialFailure, error) {
	var response httpapi.OfferListResponse
	path := "/v1/offers?workspace_id=" + url.QueryEscape(workspaceID)
	if err := client.do(ctx, http.MethodGet, path, "", nil, &response); err != nil {
		return domain.OfferSnapshot{}, nil, fmt.Errorf("list trial offers: %w", err)
	}
	if len(response.Offers) == 0 {
		return domain.OfferSnapshot{}, &TrialFailure{Code: "NO_OFFERS", Message: "provider returned no placeable offers"}, nil
	}
	sort.Slice(response.Offers, func(i, j int) bool {
		return maximumCost(response.Offers[i], trial.Timeout) < maximumCost(response.Offers[j], trial.Timeout)
	})
	for _, offer := range response.Offers {
		if offer.Pricing.Known && offer.Pricing.Currency == "USD" && maximumCost(offer, trial.Timeout) <= trial.MaxExpectedCostUSD {
			return offer, nil, nil
		}
	}
	cheapest := response.Offers[0]
	if !cheapest.Pricing.Known || cheapest.Pricing.Currency != "USD" {
		return domain.OfferSnapshot{}, &TrialFailure{Code: "PRICING_UNKNOWN", Message: "provider did not return known USD pricing"}, nil
	}
	return domain.OfferSnapshot{}, &TrialFailure{Code: "BUDGET_EXCEEDED", Message: fmt.Sprintf("cheapest offer maximum cost %.6f USD exceeds budget %.6f USD", maximumCost(cheapest, trial.Timeout), trial.MaxExpectedCostUSD)}, nil
}

func maximumCost(offer domain.OfferSnapshot, timeout time.Duration) float64 {
	return offer.Pricing.RatePerSecondUSD * timeout.Seconds()
}

func offerEvidence(offer domain.OfferSnapshot, timeout time.Duration) OfferEvidence {
	return OfferEvidence{ID: offer.ID, ConnectionID: offer.ConnectionID, RatePerSecondUSD: offer.Pricing.RatePerSecondUSD, MaximumCostUSD: maximumCost(offer, timeout)}
}

func (client trialClient) createRun(ctx context.Context, workspaceID, runID string, trial Trial, offer domain.OfferSnapshot) (httpapi.RunResponse, error) {
	request := httpapi.CreateRunRequest{WorkspaceId: workspaceID, RunId: runID, Workload: successWorkload(workspaceID, trial, offer.Platform)}
	var response httpapi.RunResponse
	if err := client.do(ctx, http.MethodPost, "/v1/runs", "run:create:"+runID, request, &response); err != nil {
		return response, fmt.Errorf("create probe Run: %w", err)
	}
	return response, nil
}

func successWorkload(workspaceID string, trial Trial, platform domain.Platform) domain.WorkloadRevision {
	resources := domain.ResourceRequirements{}
	if trial.AdapterType != "docker" {
		resources.Accelerators = []domain.AcceleratorRequirement{{Vendor: "nvidia", Count: 1}}
	}
	budget := trial.MaxExpectedCostUSD
	return domain.WorkloadRevision{
		ID: "wrev_conformance_probe", WorkspaceID: workspaceID, WorkloadID: "wrk_conformance_probe", Digest: "sha256:conformance-probe",
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{Name: "main", Image: trial.Image, Platform: platform, Args: []string{"success"}}},
			Resources:  resources,
			Placement:  domain.PlacementPolicy{Objective: domain.ObjectiveCheapest, ExpectedRuntimeSeconds: trial.Timeout.Seconds(), MaxExpectedCostUSD: &budget},
			Execution:  domain.ExecutionPolicy{MaxRuntimeSeconds: int64(trial.Timeout.Seconds()), MaxPreStartAttempts: 1},
		},
	}
}

func (client trialClient) waitClosed(ctx context.Context, workspaceID, runID string) (httpapi.RunResponse, error) {
	for {
		var response httpapi.RunResponse
		path := "/v1/runs/" + url.PathEscape(runID) + "/wait?workspace_id=" + url.QueryEscape(workspaceID)
		if err := client.do(ctx, http.MethodGet, path, "", nil, &response); err != nil {
			return response, fmt.Errorf("wait for probe Run: %w", err)
		}
		if response.Run.Closed {
			return response, nil
		}
	}
}

func (client trialClient) cancelRun(ctx context.Context, workspaceID, runID string) error {
	path := "/v1/runs/" + url.PathEscape(runID) + "/cancel?workspace_id=" + url.QueryEscape(workspaceID)
	return client.do(ctx, http.MethodPost, path, "", nil, &httpapi.RunResponse{})
}

func (client trialClient) eventTypes(ctx context.Context, workspaceID, runID string) ([]string, error) {
	var response httpapi.EventListResponse
	path := "/v1/runs/" + url.PathEscape(runID) + "/events?workspace_id=" + url.QueryEscape(workspaceID)
	if err := client.do(ctx, http.MethodGet, path, "", nil, &response); err != nil {
		return nil, err
	}
	types := make([]string, 0, len(response.Events))
	for _, event := range response.Events {
		types = append(types, event.Type)
	}
	return types, nil
}

func runEvidence(run domain.RunRecord) RunEvidence {
	return RunEvidence{ID: run.ID, Outcome: string(run.Outcome), ExitCode: run.ExitCode, Cleanup: string(run.Cleanup), Closed: run.Closed}
}

func (client trialClient) do(ctx context.Context, method, path, idempotencyKey string, body, response any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	result, err := client.client.Do(request)
	if err != nil {
		return err
	}
	defer result.Body.Close()
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(result.Body, 4096))
		return fmt.Errorf("%s %s returned %d: %s", method, path, result.StatusCode, strings.TrimSpace(string(raw)))
	}
	if response == nil {
		return nil
	}
	if err := json.NewDecoder(result.Body).Decode(response); err != nil {
		return fmt.Errorf("decode %s %s: %w", method, path, err)
	}
	return nil
}
