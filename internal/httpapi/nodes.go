package httpapi

import (
	"context"
	"errors"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/node"
)

// NodeRegistry is the operator-facing half of the node registry. The agent
// protocol is a separate handler with separate credentials; these routes are
// for the person who decides which machines Mercator may use.
type NodeRegistry interface {
	Invite(ctx context.Context, invitation node.Invitation) (capability.NodeBootstrap, error)
	List(ctx context.Context, workspaceID string) ([]node.Record, error)
}

// WithNodes enables the node endpoints. A Mercator without a node registry
// serves 404 for them rather than pretending nodes could exist.
func WithNodes(registry NodeRegistry) Option {
	return func(s *Server) { s.nodes = registry }
}

// InviteNode reserves a node identity and returns the material one machine
// needs to enroll. The enrollment token is returned exactly this once: it is
// short-lived, redeemable once, and never stored in a readable form.
func (s *Server) InviteNode(ctx context.Context, request InviteNodeRequestObject) (InviteNodeResponseObject, error) {
	if s.nodes == nil {
		return InviteNode500JSONResponse{Code: "NODES_UNAVAILABLE", Message: "This Mercator has no node registry."}, nil
	}
	body := request.Body
	if body.WorkspaceId == "" {
		return InviteNode400JSONResponse{Code: "INVALID_REQUEST", Message: "workspace_id is required."}, nil
	}
	if body.ShadowPriceUsdPerHour <= 0 {
		return InviteNode400JSONResponse{
			Code: "INVALID_REQUEST",
			Message: "shadow_price_usd_per_hour must be positive. Placement weighs a node against fresh capacity by price, " +
				"so a node with no price would be refused as unpriced rather than treated as free.",
		}, nil
	}
	if body.BillingIntervalSeconds < 0 {
		return InviteNode400JSONResponse{
			Code: "INVALID_REQUEST",
			Message: "billing_interval_seconds cannot run backwards. A machine bought in no increments at all states none, " +
				"and Mercator then holds it continuously rather than in blocks.",
		}, nil
	}
	for _, class := range body.EligibleServiceClasses {
		if !class.Known() {
			return InviteNode400JSONResponse{
				Code: "INVALID_REQUEST",
				Message: "eligible_service_classes names \"" + string(class) + "\", which Mercator cannot price. " +
					"Holding a machine for work Mercator refuses at the door is holding it for nothing.",
			}, nil
		}
	}
	invitation := node.Invitation{
		WorkspaceID: body.WorkspaceId,
		NodeID:      body.NodeId,
		RentalID:    body.RentalId,
		Generation:  1,
		// What this machine costs and the rest of what it is bought on. The rate is one
		// term of what a placement here spends: a machine billed in whole hours costs
		// the hour whatever a Run uses of it, a machine an operator holds for watched
		// work refuses every other class, and a machine that goes back to its owner at
		// a stated moment takes no work that could still be running then.
		ShadowPriceUSDPerHour: float64(body.ShadowPriceUsdPerHour),
		Purchase: node.Purchase{
			BillingIntervalSeconds: body.BillingIntervalSeconds,
			EligibleClasses:        body.EligibleServiceClasses,
			AvailableUntil:         body.AvailableUntil.UTC(),
		},
	}
	bootstrap, err := s.nodes.Invite(ctx, invitation)
	if err != nil {
		if errors.Is(err, node.ErrIdentityExists) {
			return InviteNode409JSONResponse{Code: "NODE_EXISTS", Message: err.Error()}, nil
		}
		return InviteNode500JSONResponse{Code: "NODE_INVITE_FAILED", Message: err.Error()}, nil
	}
	return InviteNode201JSONResponse{
		ControlPlaneUrl: bootstrap.ControlPlaneURL,
		NodeId:          bootstrap.NodeID,
		RentalId:        bootstrap.RentalID,
		Generation:      int64(bootstrap.Generation),
		EnrollmentToken: bootstrap.EnrollmentToken,
		AgentVersion:    bootstrap.AgentVersion,
	}, nil
}

// ListNodes reports every node identity in a workspace, whatever its state, so
// an operator sees capacity that never enrolled as readily as capacity that
// did.
func (s *Server) ListNodes(ctx context.Context, request ListNodesRequestObject) (ListNodesResponseObject, error) {
	if s.nodes == nil {
		return ListNodes500JSONResponse{Code: "NODES_UNAVAILABLE", Message: "This Mercator has no node registry."}, nil
	}
	if request.Params.WorkspaceId == "" {
		return ListNodes400JSONResponse{Code: "INVALID_REQUEST", Message: "workspace_id is required."}, nil
	}
	records, err := s.nodes.List(ctx, request.Params.WorkspaceId)
	if err != nil {
		return ListNodes500JSONResponse{Code: "NODE_LIST_FAILED", Message: err.Error()}, nil
	}
	summaries := make([]NodeSummary, 0, len(records))
	for _, record := range records {
		summaries = append(summaries, nodeSummary(record))
	}
	return ListNodes200JSONResponse{Nodes: summaries}, nil
}

func nodeSummary(record node.Record) NodeSummary {
	accelerators := 0
	for _, accelerator := range record.Facts.Host.Accelerator.Devices {
		accelerators += accelerator.Count
	}
	summary := NodeSummary{
		Id:                    record.ID,
		RentalId:              record.RentalID,
		Generation:            int64(record.Generation),
		State:                 NodeSummaryState(record.State),
		ShadowPriceUsdPerHour: float32(record.ShadowPriceUSDPerHour),
	}
	summary.AgentVersion = record.AgentVersion
	summary.ContainerRuntime = record.Facts.Host.ContainerRuntime
	summary.Accelerators = accelerators
	summary.LeaseExpires = record.LeaseExpires
	summary.LastHeartbeatAt = record.LastHeartbeatAt
	// A node states the room it established and separately what is known about
	// the measurement, because the answers send an operator to different places:
	// a full machine is one to clear out, a machine that cannot be measured is a
	// daemon this agent is not beside, and a machine nobody has heard from is
	// one to go and start an agent on.
	summary.DiskReport = NodeSummaryDiskReport(record.Disk())
	summary.DiskFreeBytes = record.Facts.Host.Disk.FreeBytes
	return summary
}
