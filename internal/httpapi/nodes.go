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
	invitation := node.Invitation{
		WorkspaceID:           body.WorkspaceId,
		NodeID:                body.NodeId,
		RentalID:              body.RentalId,
		Generation:            1,
		ShadowPriceUSDPerHour: float64(body.ShadowPriceUsdPerHour),
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
	for _, accelerator := range record.Facts.Host.Accelerators {
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
	return summary
}
