package httpapi

import (
	"context"

	"github.com/benngarcia/mercator/internal/eventlog"
)

type workspaceTestLog struct {
	eventlog.EventLog
}

func (l workspaceTestLog) AppendNew(ctx context.Context, request eventlog.AppendRequest) (eventlog.AppendResult, error) {
	return l.Append(ctx, request)
}
