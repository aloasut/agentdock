package observability

import (
	"context"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const MaxStagesPerExecution = 16

type Stage string

const (
	StageMCPRefresh            Stage = "mcp.refresh"
	StageMCPRemoteCall         Stage = "mcp.remote_call"
	StageCommandStart          Stage = "command.start"
	StageCommandForegroundWait Stage = "command.foreground_wait"
)

// StageRecord 只描述一次 Tool 调用中的固定阶段耗时。
// 不允许记录参数、结果、命令、路径、错误正文或其他 Payload。
type StageRecord struct {
	Name            Stage   `json:"name"`
	StartedOffsetMS float64 `json:"started_offset_ms"`
	DurationMS      float64 `json:"duration_ms"`
	Success         bool    `json:"success"`
}

type executionContextKey struct{}

type executionState struct {
	mu sync.Mutex

	startedAt time.Time
	closed    bool
	stages    []StageRecord
}

// WithExecution 为当前 Tool 调用附加本进程阶段收集状态。
// 该状态只在 Runtime.Call 生命周期内有效，不承担跨进程 tracing。
func WithExecution(ctx context.Context, startedAt time.Time) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return context.WithValue(ctx, executionContextKey{}, &executionState{
		startedAt: startedAt,
		stages:    make([]StageRecord, 0, 4),
	})
}

// RecordStage 记录固定枚举阶段。未知名称、缺失 execution 或已结束调用都会安全忽略。
func RecordStage(ctx context.Context, name Stage, startedAt time.Time, success bool) {
	state := executionFromContext(ctx)
	if state == nil || !validStage(name) {
		return
	}
	finishedAt := time.Now()
	if startedAt.IsZero() {
		startedAt = finishedAt
	}
	offset := startedAt.Sub(state.startedAt)
	if offset < 0 {
		offset = 0
	}
	duration := finishedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || len(state.stages) >= MaxStagesPerExecution {
		return
	}
	state.stages = append(state.stages, StageRecord{
		Name:            name,
		StartedOffsetMS: durationMilliseconds(offset),
		DurationMS:      durationMilliseconds(duration),
		Success:         success,
	})
}

// CloseExecution 关闭当前调用的阶段收集并返回按开始时间排序的快照。
// 后续异步 Goroutine 的迟到打点会被忽略，避免 child stage 超出父 Tool 生命周期。
// AddStageEvents projects the existing lightweight stage model into OpenTelemetry
// without creating another child-span tree. Event timestamps are the actual stage
// completion times derived from the parent Tool start.
func AddStageEvents(span trace.Span, toolStartedAt time.Time, stages []StageRecord) {
	if span == nil || !span.IsRecording() || toolStartedAt.IsZero() {
		return
	}
	for _, stage := range stages {
		completedAt := toolStartedAt.Add(time.Duration((stage.StartedOffsetMS + stage.DurationMS) * float64(time.Millisecond)))
		span.AddEvent(
			string(stage.Name),
			trace.WithTimestamp(completedAt),
			trace.WithAttributes(
				attribute.Float64("agentdock.stage.duration_ms", stage.DurationMS),
				attribute.Bool("agentdock.stage.success", stage.Success),
			),
		)
	}
}

func CloseExecution(ctx context.Context) []StageRecord {
	state := executionFromContext(ctx)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	state.closed = true
	stages := append([]StageRecord(nil), state.stages...)
	state.mu.Unlock()

	sort.SliceStable(stages, func(i, j int) bool {
		return stages[i].StartedOffsetMS < stages[j].StartedOffsetMS
	})
	return stages
}

func executionFromContext(ctx context.Context) *executionState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(executionContextKey{}).(*executionState)
	return state
}

func validStage(name Stage) bool {
	switch name {
	case StageMCPRefresh, StageMCPRemoteCall, StageCommandStart, StageCommandForegroundWait:
		return true
	default:
		return false
	}
}
