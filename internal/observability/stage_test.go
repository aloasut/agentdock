package observability

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExecutionStagesAreBoundedAndIgnoreLateWrites(t *testing.T) {
	startedAt := time.Now()
	ctx := WithExecution(context.Background(), startedAt)
	for index := 0; index < MaxStagesPerExecution+5; index++ {
		RecordStage(ctx, StageMCPRemoteCall, startedAt.Add(time.Duration(index)*time.Microsecond), true)
	}
	stages := CloseExecution(ctx)
	if len(stages) != MaxStagesPerExecution {
		t.Fatalf("stage count = %d, want %d", len(stages), MaxStagesPerExecution)
	}

	RecordStage(ctx, StageCommandStart, time.Now(), true)
	if got := CloseExecution(ctx); len(got) != MaxStagesPerExecution {
		t.Fatalf("late stage changed closed execution: %d", len(got))
	}
}

func TestExecutionStagesIgnoreUnknownAndMissingContext(t *testing.T) {
	RecordStage(context.Background(), StageMCPRemoteCall, time.Now(), true)

	ctx := WithExecution(context.Background(), time.Now())
	RecordStage(ctx, Stage("user-data"), time.Now(), true)
	if stages := CloseExecution(ctx); len(stages) != 0 {
		t.Fatalf("unknown stage was retained: %#v", stages)
	}
}

func TestExecutionStagesAreConcurrentSafe(t *testing.T) {
	ctx := WithExecution(context.Background(), time.Now())
	var wait sync.WaitGroup
	for index := 0; index < 64; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			RecordStage(ctx, StageCommandStart, time.Now(), true)
		}()
	}
	wait.Wait()
	if stages := CloseExecution(ctx); len(stages) != MaxStagesPerExecution {
		t.Fatalf("concurrent stage count = %d, want %d", len(stages), MaxStagesPerExecution)
	}
}

func TestStageRecordJSONKeepsZeroPayloadBoundary(t *testing.T) {
	stage := StageRecord{Name: StageCommandStart, StartedOffsetMS: 1.5, DurationMS: 2.5, Success: false}
	encoded, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(string(encoded))
	for _, forbidden := range []string{"arguments", "result", "output", "details", "message", "command", "path", "error", "metadata"} {
		if strings.Contains(body, `"`+forbidden+`"`) {
			t.Fatalf("stage JSON contains forbidden field %q: %s", forbidden, body)
		}
	}
}
