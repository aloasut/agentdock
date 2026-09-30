package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/uvwt/agentdock/internal/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// observeToolCall 只记录固定白名单元数据。参数、结果、错误正文和 Details
// 都不能进入运行分析，避免诊断能力反过来扩大敏感数据暴露面。
func (r *Runtime) observeToolCall(parentCtx, ctx context.Context, name string, startedAt time.Time, callErr *error) {
	recovered := recover()
	success := recovered == nil && (callErr == nil || *callErr == nil)
	errorCode := ""
	errorCategory := ""
	if recovered != nil {
		errorCode = "PANIC"
		errorCategory = "runtime"
	} else if callErr != nil && *callErr != nil {
		errorCode, errorCategory = observableError(*callErr)
	}

	toolName := "unknown"
	if r != nil {
		if _, available := r.toolValidators[name]; available {
			toolName = name
		}
	}
	source := observability.SourceFromContext(ctx)
	duration := time.Since(startedAt)
	stages := observability.CloseExecution(ctx)
	traceID, spanID := observability.TraceIdentifiers(parentCtx, ctx)
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		observability.AddStageEvents(span, startedAt, stages)
		span.SetAttributes(
			attribute.String("agentdock.tool.name", toolName),
			attribute.String("agentdock.tool.source", string(source)),
			attribute.Bool("agentdock.tool.success", success),
		)
		if errorCode != "" {
			span.SetAttributes(
				attribute.String("error.type", errorCode),
				attribute.String("agentdock.error.category", errorCategory),
			)
			span.SetStatus(codes.Error, errorCode)
		}
	}
	if r != nil {
		r.observer.EndTool(toolName, source, startedAt, duration, success, errorCode, errorCategory, traceID, spanID, stages)
	}

	attributes := []any{
		"tool", toolName,
		"source", string(source),
		"duration_ms", float64(duration) / float64(time.Millisecond),
		"ok", success,
	}
	if errorCode != "" {
		attributes = append(attributes, "error_code", errorCode, "error_category", errorCategory)
	}
	if traceID != "" {
		attributes = append(attributes, "trace_id", traceID)
		if spanID != "" {
			attributes = append(attributes, "span_id", spanID)
		}
	}
	slog.Info("tool finished", attributes...)

	if recovered != nil {
		panic(recovered)
	}
}

func observableError(err error) (string, string) {
	var toolErr *ToolError
	if errors.As(err, &toolErr) {
		return toolErr.Code, toolErr.Category
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "DEADLINE_EXCEEDED", "runtime"
	}
	if errors.Is(err, context.Canceled) {
		return "CANCELED", "runtime"
	}
	return "UNCLASSIFIED", "runtime"
}
