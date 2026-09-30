package observability

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRecentCallsAndDiagnosticsProjectionStayZeroPayload(t *testing.T) {
	recorder := NewRecorder(4)
	recorder.BeginTool()
	recorder.EndTool(
		"exec_command",
		SourceNexus,
		time.Unix(10, 0),
		25*time.Millisecond,
		false,
		"COMMAND_FAILED",
		"runtime",
		"4bf92f3577b34da6a3ce929d0e0e4736",
		"00f067aa0ba902b7",
		[]StageRecord{{Name: StageCommandStart, StartedOffsetMS: 2, DurationMS: 3, Success: true}},
	)

	projected := ProjectDiagnostics(recorder.RecentCalls())
	if len(projected) != 1 {
		t.Fatalf("projected calls = %d", len(projected))
	}
	call := projected[0]
	if call.ID != "1" || call.Tool != "exec_command" || call.TraceID == "" || len(call.Stages) != 1 {
		t.Fatalf("diagnostic call = %#v", call)
	}

	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(string(encoded))
	for _, forbidden := range []string{
		"span_id", "arguments", "result", "output", "stdout", "stderr",
		"command", "path", "error_message", "stack", "tool_stats", "process",
	} {
		if strings.Contains(body, `"`+forbidden+`"`) {
			t.Fatalf("diagnostics JSON contains forbidden field %q: %s", forbidden, body)
		}
	}
}

func TestRecentCallsReturnsIndependentStageCopy(t *testing.T) {
	recorder := NewRecorder(2)
	recorder.BeginTool()
	recorder.EndTool(
		"tool", SourceMCP, time.Now(), time.Millisecond, true, "", "", "", "",
		[]StageRecord{{Name: StageMCPRefresh, DurationMS: 1, Success: true}},
	)
	first := recorder.RecentCalls()
	first[0].Stages[0].DurationMS = 999
	second := recorder.RecentCalls()
	if second[0].Stages[0].DurationMS != 1 {
		t.Fatalf("stored stage mutated through caller copy: %#v", second[0].Stages[0])
	}
}
