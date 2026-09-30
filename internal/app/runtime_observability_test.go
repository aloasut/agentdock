package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/observability"
	"go.opentelemetry.io/otel/trace"
)

func TestRuntimeAnalyticsRecordsSafeToolMetadata(t *testing.T) {
	runtime := newRuntimeValidationTestRuntime(t)
	ctx := observability.WithSource(context.Background(), observability.SourceMCP)
	secret := "/Users/example/private/token-file"

	if _, err := runtime.Call(ctx, "agentdock_context", map[string]any{"future_field": secret}); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := runtime.Call(ctx, "secret-tool-"+secret, nil); err == nil {
		t.Fatal("expected unknown tool error")
	}

	snapshot := runtime.observer.Snapshot()
	if snapshot.TotalCalls != 2 || snapshot.TotalErrors != 2 || snapshot.WindowCalls != 2 {
		t.Fatalf("analytics totals = calls %d errors %d window %d", snapshot.TotalCalls, snapshot.TotalErrors, snapshot.WindowCalls)
	}
	if snapshot.RecentCalls[0].Tool != "unknown" {
		t.Fatalf("unknown tool name was retained: %#v", snapshot.RecentCalls[0])
	}
	known := snapshot.RecentCalls[1]
	if known.TraceID != "" || known.SpanID != "" {
		t.Fatalf("default no-op call unexpectedly created trace identifiers = %q / %q", known.TraceID, known.SpanID)
	}
	if known.Tool != "agentdock_context" || known.Source != observability.SourceMCP {
		t.Fatalf("known call metadata = %#v", known)
	}
	if known.ErrorCode != "INVALID_ARGUMENT" || known.ErrorCategory != "validation" {
		t.Fatalf("known call error metadata = %#v", known)
	}

	encoded, err := json.Marshal(runtime.RuntimeAnalytics())
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, forbidden := range []string{secret, "future_field", "secret-tool-"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("runtime analytics leaked %q: %s", forbidden, body)
		}
	}
}

func TestRuntimeAnalyticsIncludesCommandStages(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("test command uses POSIX shell syntax")
	}
	rt := newRuntimeValidationTestRuntime(t)
	result, err := rt.Call(context.Background(), "exec_command", map[string]any{
		"cmd":            "sleep 0.02",
		"execution_mode": "sync",
		"timeout_ms":     2000,
	})
	if err != nil {
		t.Fatalf("exec_command: %v", err)
	}
	if result["status"] != "exited" {
		t.Fatalf("exec_command result = %#v", result)
	}

	record := rt.observer.Snapshot().RecentCalls[0]
	if record.Tool != "exec_command" || len(record.Stages) != 2 {
		t.Fatalf("command analytics = %#v", record)
	}
	if record.Stages[0].Name != observability.StageCommandStart ||
		record.Stages[1].Name != observability.StageCommandForegroundWait {
		t.Fatalf("command stages = %#v", record.Stages)
	}
}

func TestRuntimeToolLogOmitsTraceIdentifiersWithoutSDK(t *testing.T) {
	rt := newRuntimeValidationTestRuntime(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	ctx := observability.WithSource(context.Background(), observability.SourceMCP)
	if _, err := rt.Call(ctx, "agentdock_context", map[string]any{}); err != nil {
		t.Fatalf("agentdock_context: %v", err)
	}
	record := rt.observer.Snapshot().RecentCalls[0]
	if record.TraceID != "" || record.SpanID != "" {
		t.Fatalf("default no-op call unexpectedly created trace identifiers: %#v", record)
	}
	body := logs.String()
	for _, forbidden := range []string{"\"trace_id\"", "\"span_id\""} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("tool log unexpectedly contains %s: %s", forbidden, body)
		}
	}
}

func TestRuntimeToolLogCorrelatesRemoteTraceWithoutFakeChildSpan(t *testing.T) {
	rt := newRuntimeValidationTestRuntime(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	parentSpanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: parentSpanID, TraceFlags: trace.FlagsSampled, Remote: true,
	})
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), parent)
	ctx = observability.WithSource(ctx, observability.SourceNexus)
	if _, err := rt.Call(ctx, "agentdock_context", map[string]any{}); err != nil {
		t.Fatalf("agentdock_context: %v", err)
	}

	record := rt.observer.Snapshot().RecentCalls[0]
	if record.TraceID != traceID.String() || record.SpanID != "" {
		t.Fatalf("remote correlation record = %#v", record)
	}
	body := logs.String()
	if !strings.Contains(body, "\"trace_id\":\""+traceID.String()+"\"") {
		t.Fatalf("tool log missing remote trace id: %s", body)
	}
	if strings.Contains(body, "\"span_id\"") {
		t.Fatalf("tool log invented a local child span id: %s", body)
	}
}

func TestRuntimeDiagnosticsExposeOnlyRemoteSafeRecentCalls(t *testing.T) {
	runtime := newRuntimeValidationTestRuntime(t)
	ctx := observability.WithSource(context.Background(), observability.SourceNexus)
	secret := "/Users/example/private/secret.txt"
	if _, err := runtime.Call(ctx, "agentdock_context", map[string]any{"future_field": secret}); err == nil {
		t.Fatal("expected validation error")
	}

	encoded, err := json.Marshal(runtime.RuntimeDiagnostics())
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(string(encoded))
	for _, forbidden := range []string{
		strings.ToLower(secret), "span_id", "tool_stats", "process", "p50_duration_ms",
		"arguments", "output", "command", "path", "error_message", "stack",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("runtime diagnostics leaked %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"error_code":"invalid_argument"`) {
		t.Fatalf("runtime diagnostics missing stable error code: %s", body)
	}
}
