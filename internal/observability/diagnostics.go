package observability

import (
	"strconv"
	"time"
)

// DiagnosticStage 是远程诊断允许跨进程暴露的阶段字段。
// 它有意与 StageRecord 分离，避免未来给本地阶段模型增加字段时扩大远程契约。
type DiagnosticStage struct {
	Name            string  `json:"name"`
	StartedOffsetMS float64 `json:"started_offset_ms"`
	DurationMS      float64 `json:"duration_ms"`
	Success         bool    `json:"success"`
}

// DiagnosticCall 是 Nexus 远程诊断消费的最小调用视图。
// 不包含 SpanID、参数、结果、命令、路径、错误正文或其他 Payload。
type DiagnosticCall struct {
	ID            string            `json:"id"`
	Tool          string            `json:"tool"`
	Source        string            `json:"source"`
	TraceID       string            `json:"trace_id,omitempty"`
	StartedAt     time.Time         `json:"started_at"`
	DurationMS    float64           `json:"duration_ms"`
	Success       bool              `json:"success"`
	ErrorCode     string            `json:"error_code,omitempty"`
	ErrorCategory string            `json:"error_category,omitempty"`
	Stages        []DiagnosticStage `json:"stages,omitempty"`
}

// ProjectDiagnostics 把本地运行分析模型裁剪成稳定的远程诊断契约。
func ProjectDiagnostics(records []ExecutionRecord) []DiagnosticCall {
	out := make([]DiagnosticCall, 0, len(records))
	for _, record := range records {
		stages := make([]DiagnosticStage, 0, len(record.Stages))
		for _, stage := range record.Stages {
			stages = append(stages, DiagnosticStage{
				Name:            string(stage.Name),
				StartedOffsetMS: stage.StartedOffsetMS,
				DurationMS:      stage.DurationMS,
				Success:         stage.Success,
			})
		}
		out = append(out, DiagnosticCall{
			ID:            strconv.FormatUint(record.ID, 10),
			Tool:          record.Tool,
			Source:        string(record.Source),
			TraceID:       record.TraceID,
			StartedAt:     record.StartedAt,
			DurationMS:    record.DurationMS,
			Success:       record.Success,
			ErrorCode:     record.ErrorCode,
			ErrorCategory: record.ErrorCategory,
			Stages:        stages,
		})
	}
	return out
}
