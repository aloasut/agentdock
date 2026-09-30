package observability

import (
	"math"
	"runtime"
	"sort"
	"sync"
	"time"
)

const DefaultRecentCapacity = 500

// ExecutionRecord 是运行分析保存的最小工具调用元数据。
// 这里有意不包含参数、结果、错误正文或路径等 Payload。
type ExecutionRecord struct {
	ID            uint64        `json:"id"`
	Tool          string        `json:"tool"`
	Source        Source        `json:"source"`
	TraceID       string        `json:"trace_id,omitempty"`
	SpanID        string        `json:"span_id,omitempty"`
	StartedAt     time.Time     `json:"started_at"`
	DurationMS    float64       `json:"duration_ms"`
	Success       bool          `json:"success"`
	ErrorCode     string        `json:"error_code,omitempty"`
	ErrorCategory string        `json:"error_category,omitempty"`
	Stages        []StageRecord `json:"stages,omitempty"`
}

type ToolStats struct {
	Tool          string  `json:"tool"`
	Count         int     `json:"count"`
	ErrorCount    int     `json:"error_count"`
	ErrorRate     float64 `json:"error_rate"`
	P50DurationMS float64 `json:"p50_duration_ms"`
	P95DurationMS float64 `json:"p95_duration_ms"`
	P99DurationMS float64 `json:"p99_duration_ms"`
}

type ProcessSnapshot struct {
	Goroutines     int    `json:"goroutines"`
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	HeapInuseBytes uint64 `json:"heap_inuse_bytes"`
	HeapSysBytes   uint64 `json:"heap_sys_bytes"`
	GCCycles       uint32 `json:"gc_cycles"`
	UptimeMS       int64  `json:"uptime_ms"`
}

type Snapshot struct {
	StartedAt      time.Time         `json:"started_at"`
	RecentCapacity int               `json:"recent_capacity"`
	WindowCalls    int               `json:"window_calls"`
	TotalCalls     uint64            `json:"total_calls"`
	TotalErrors    uint64            `json:"total_errors"`
	ActiveCalls    int               `json:"active_calls"`
	ToolStats      []ToolStats       `json:"tool_stats"`
	RecentCalls    []ExecutionRecord `json:"recent_calls"`
	Process        ProcessSnapshot   `json:"process"`
}

type Recorder struct {
	mu sync.RWMutex

	startedAt time.Time
	records   []ExecutionRecord
	capacity  int
	next      int
	size      int

	nextID      uint64
	totalCalls  uint64
	totalErrors uint64
	activeCalls int
}

func NewRecorder(capacity int) *Recorder {
	if capacity <= 0 {
		capacity = DefaultRecentCapacity
	}
	return &Recorder{
		startedAt: time.Now(),
		records:   make([]ExecutionRecord, capacity),
		capacity:  capacity,
	}
}

func (r *Recorder) BeginTool() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.activeCalls++
	r.mu.Unlock()
}

func (r *Recorder) EndTool(
	tool string,
	source Source,
	startedAt time.Time,
	duration time.Duration,
	success bool,
	errorCode string,
	errorCategory string,
	traceID string,
	spanID string,
	stages []StageRecord,
) {
	if r == nil {
		return
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	if duration < 0 {
		duration = 0
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.activeCalls > 0 {
		r.activeCalls--
	}
	if r.capacity <= 0 || len(r.records) == 0 {
		return
	}

	r.nextID++
	r.totalCalls++
	if !success {
		r.totalErrors++
	}

	record := ExecutionRecord{
		ID:            r.nextID,
		Tool:          tool,
		Source:        normalizedSource(source),
		StartedAt:     startedAt.UTC(),
		DurationMS:    durationMilliseconds(duration),
		Success:       success,
		ErrorCode:     errorCode,
		ErrorCategory: errorCategory,
		TraceID:       traceID,
		SpanID:        spanID,
		Stages:        append([]StageRecord(nil), stages...),
	}
	r.records[r.next] = record
	r.next = (r.next + 1) % r.capacity
	if r.size < r.capacity {
		r.size++
	}
}

// RecentCalls 返回当前内存环里最近调用的独立副本，不计算百分位或进程指标。
func (r *Recorder) RecentCalls() []ExecutionRecord {
	if r == nil {
		return []ExecutionRecord{}
	}
	r.mu.RLock()
	recent := r.recentLocked()
	r.mu.RUnlock()
	for i := range recent {
		recent[i].Stages = append([]StageRecord(nil), recent[i].Stages...)
	}
	return recent
}

func (r *Recorder) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}

	r.mu.RLock()
	startedAt := r.startedAt
	capacity := r.capacity
	totalCalls := r.totalCalls
	totalErrors := r.totalErrors
	activeCalls := r.activeCalls
	recent := r.recentLocked()
	r.mu.RUnlock()

	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)

	return Snapshot{
		StartedAt:      startedAt.UTC(),
		RecentCapacity: capacity,
		WindowCalls:    len(recent),
		TotalCalls:     totalCalls,
		TotalErrors:    totalErrors,
		ActiveCalls:    activeCalls,
		ToolStats:      aggregateToolStats(recent),
		RecentCalls:    recent,
		Process: ProcessSnapshot{
			Goroutines:     runtime.NumGoroutine(),
			HeapAllocBytes: memory.HeapAlloc,
			HeapInuseBytes: memory.HeapInuse,
			HeapSysBytes:   memory.HeapSys,
			GCCycles:       memory.NumGC,
			UptimeMS:       maxInt64(0, time.Since(startedAt).Milliseconds()),
		},
	}
}

func (r *Recorder) recentLocked() []ExecutionRecord {
	if r.size == 0 {
		return []ExecutionRecord{}
	}
	recent := make([]ExecutionRecord, 0, r.size)
	for offset := 0; offset < r.size; offset++ {
		index := (r.next - 1 - offset + r.capacity) % r.capacity
		recent = append(recent, r.records[index])
	}
	return recent
}

func aggregateToolStats(records []ExecutionRecord) []ToolStats {
	type accumulator struct {
		count      int
		errorCount int
		durations  []float64
	}
	byTool := make(map[string]*accumulator)
	for _, record := range records {
		current := byTool[record.Tool]
		if current == nil {
			current = &accumulator{}
			byTool[record.Tool] = current
		}
		current.count++
		if !record.Success {
			current.errorCount++
		}
		current.durations = append(current.durations, record.DurationMS)
	}

	stats := make([]ToolStats, 0, len(byTool))
	for tool, current := range byTool {
		sort.Float64s(current.durations)
		stats = append(stats, ToolStats{
			Tool:          tool,
			Count:         current.count,
			ErrorCount:    current.errorCount,
			ErrorRate:     float64(current.errorCount) / float64(current.count),
			P50DurationMS: percentile(current.durations, 0.50),
			P95DurationMS: percentile(current.durations, 0.95),
			P99DurationMS: percentile(current.durations, 0.99),
		})
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Count != stats[j].Count {
			return stats[i].Count > stats[j].Count
		}
		return stats[i].Tool < stats[j].Tool
	})
	return stats
}

// percentile 使用 nearest-rank 定义：P95 表示至少 95% 的样本不大于该值。
func percentile(sorted []float64, quantile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if quantile <= 0 {
		return sorted[0]
	}
	if quantile >= 1 {
		return sorted[len(sorted)-1]
	}
	index := int(math.Ceil(quantile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func durationMilliseconds(duration time.Duration) float64 {
	value := float64(duration) / float64(time.Millisecond)
	return math.Round(value*1000) / 1000
}

func normalizedSource(source Source) Source {
	switch source {
	case SourceMCP, SourceNexus, SourceInternal:
		return source
	default:
		return SourceInternal
	}
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
