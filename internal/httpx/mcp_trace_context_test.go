package httpx

import (
	"context"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestExtractMCPTraceContextPreservesRemoteParent(t *testing.T) {
	header := http.Header{}
	header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	header.Set("tracestate", "rojo=00f067aa0ba902b7")
	ctx := extractMCPTraceContext(context.Background(), header)
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() || !spanContext.IsRemote() || !spanContext.IsSampled() {
		t.Fatalf("span context = %#v", spanContext)
	}
	if spanContext.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" || spanContext.SpanID().String() != "00f067aa0ba902b7" {
		t.Fatalf("ids = %s / %s", spanContext.TraceID(), spanContext.SpanID())
	}
	if spanContext.TraceState().String() != "rojo=00f067aa0ba902b7" {
		t.Fatalf("tracestate = %q", spanContext.TraceState())
	}
}

func TestExtractMCPTraceContextIgnoresInvalidHeader(t *testing.T) {
	header := http.Header{"Traceparent": []string{"invalid"}}
	ctx := extractMCPTraceContext(context.Background(), header)
	if got := trace.SpanContextFromContext(ctx); got.IsValid() {
		t.Fatalf("invalid header produced span context: %#v", got)
	}
}
