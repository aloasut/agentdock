package observability

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestTracingDefaultsToNoopAndPreservesRemoteParent(t *testing.T) {
	tracing := NewTracing(nil)

	rootCtx, rootSpan := tracing.StartTool(context.Background(), SourceMCP)
	rootTraceID, rootSpanID := TraceIdentifiers(context.Background(), rootCtx)
	rootSpan.End()
	if rootTraceID != "" || rootSpanID != "" {
		t.Fatalf("default no-op root identifiers = %q / %q", rootTraceID, rootSpanID)
	}

	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatal(err)
	}
	state, err := trace.ParseTraceState("rojo=00f067aa0ba902b7")
	if err != nil {
		t.Fatal(err)
	}
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, TraceState: state, Remote: true,
	})
	parentCtx := trace.ContextWithRemoteSpanContext(context.Background(), parent)
	activeCtx, span := tracing.StartTool(parentCtx, SourceNexus)
	defer span.End()

	active := trace.SpanContextFromContext(activeCtx)
	if !active.Equal(parent) {
		t.Fatalf("default no-op span context = %#v, want remote parent %#v", active, parent)
	}
	gotTraceID, gotSpanID := TraceIdentifiers(parentCtx, activeCtx)
	if gotTraceID != traceID.String() || gotSpanID != "" {
		t.Fatalf("correlation identifiers = %q / %q", gotTraceID, gotSpanID)
	}
}

type recordedEvent struct {
	name string
	time time.Time
}

type recordingSpan struct {
	trace.Span
	events []recordedEvent
}

func newRecordingSpan() *recordingSpan {
	return &recordingSpan{Span: noop.Span{}}
}

func (s *recordingSpan) IsRecording() bool { return true }

func (s *recordingSpan) AddEvent(name string, options ...trace.EventOption) {
	cfg := trace.NewEventConfig(options...)
	s.events = append(s.events, recordedEvent{name: name, time: cfg.Timestamp()})
}

func TestAddStageEventsUsesOriginalStageTiming(t *testing.T) {
	span := newRecordingSpan()
	startedAt := time.Now().Add(-time.Second)
	AddStageEvents(span, startedAt, []StageRecord{{
		Name: StageCommandStart, StartedOffsetMS: 10, DurationMS: 5, Success: true,
	}})
	if len(span.events) != 1 {
		t.Fatalf("events = %#v", span.events)
	}
	event := span.events[0]
	if event.name != string(StageCommandStart) {
		t.Fatalf("event name = %q", event.name)
	}
	want := startedAt.Add(15 * time.Millisecond)
	if event.time.Sub(want) > time.Microsecond || want.Sub(event.time) > time.Microsecond {
		t.Fatalf("event time = %v, want %v", event.time, want)
	}
}

func TestAddStageEventsSkipsNoopSpan(t *testing.T) {
	span := noop.Span{}
	AddStageEvents(span, time.Now(), []StageRecord{{
		Name: StageCommandStart, DurationMS: 1, Success: true,
	}})
}
