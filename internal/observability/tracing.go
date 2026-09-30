package observability

import (
	"context"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const instrumentationName = "github.com/uvwt/agentdock"

// Tracing 只持有 OpenTelemetry API 层的 Tracer。
// 默认使用 no-op 实现，不创建 SDK Provider，也不拥有采样、导出或 Shutdown 生命周期。
type Tracing struct {
	tracer trace.Tracer
}

func NewTracing(tracer trace.Tracer) *Tracing {
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer(instrumentationName)
	}
	return &Tracing{tracer: tracer}
}

func (t *Tracing) StartTool(ctx context.Context, source Source) (context.Context, trace.Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || t.tracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	kind := trace.SpanKindInternal
	if source == SourceMCP || source == SourceNexus {
		kind = trace.SpanKindServer
	}
	return t.tracer.Start(ctx, "agentdock.tool", trace.WithSpanKind(kind))
}

// TraceIdentifiers 返回当前 TraceID 与“本次调用实际创建的”SpanID。
// 默认 no-op tracer 会原样保留上游 SpanContext；这种情况下只关联 TraceID，
// 不把上游 SpanID 冒充成本地 child span。
func TraceIdentifiers(parentCtx, activeCtx context.Context) (string, string) {
	if activeCtx == nil {
		return "", ""
	}
	active := trace.SpanContextFromContext(activeCtx)
	if !active.IsValid() {
		return "", ""
	}

	traceID := active.TraceID().String()
	if parentCtx == nil {
		return traceID, active.SpanID().String()
	}
	parent := trace.SpanContextFromContext(parentCtx)
	if parent.IsValid() &&
		active.TraceID() == parent.TraceID() &&
		active.SpanID() == parent.SpanID() {
		return traceID, ""
	}
	return traceID, active.SpanID().String()
}
