package nexusbridge

import (
	"context"
	"testing"

	protocol "github.com/uvwt/agentdock-protocol"
	"go.opentelemetry.io/otel/trace"
)

func TestExtractBridgeTraceContextPreservesW3CParent(t *testing.T) {
	message := &protocol.Message{
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Tracestate:  "rojo=00f067aa0ba902b7",
	}
	ctx := extractBridgeTraceContext(context.Background(), message)
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() || !spanContext.IsRemote() || !spanContext.IsSampled() {
		t.Fatalf("extracted span context = %#v", spanContext)
	}
	if spanContext.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" || spanContext.SpanID().String() != "00f067aa0ba902b7" {
		t.Fatalf("extracted ids = %s / %s", spanContext.TraceID(), spanContext.SpanID())
	}
	if spanContext.TraceState().String() != message.Tracestate {
		t.Fatalf("tracestate = %q", spanContext.TraceState())
	}
}

func TestExtractBridgeTraceContextIgnoresInvalidParent(t *testing.T) {
	ctx := extractBridgeTraceContext(context.Background(), &protocol.Message{Traceparent: "invalid"})
	if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
		t.Fatalf("invalid traceparent produced span context: %#v", spanContext)
	}
}
