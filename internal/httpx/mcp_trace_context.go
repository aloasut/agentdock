package httpx

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel/propagation"
)

var mcpTraceContext = propagation.TraceContext{}

func extractMCPTraceContext(ctx context.Context, header http.Header) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return mcpTraceContext.Extract(ctx, propagation.HeaderCarrier(header))
}
