package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/app"
)

func TestAnalyticsPageRendersChineseAndSecurityHeaders(t *testing.T) {
	handler := loopbackOnly(analyticsPageHandler())
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:27123/analytics", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1:27123"
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "运行分析") || !strings.Contains(recorder.Body.String(), "阶段") {
		t.Fatalf("page missing Chinese analytics labels: %s", recorder.Body.String())
	}
	for _, required := range []string{"stage-toggle", "stage-detail", "started_offset_ms"} {
		if !strings.Contains(recorder.Body.String(), required) {
			t.Fatalf("page missing stage UI contract %q", required)
		}
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control = %q", recorder.Header().Get("Cache-Control"))
	}
	if !strings.Contains(recorder.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("unexpected CSP: %s", recorder.Header().Get("Content-Security-Policy"))
	}
}

func TestAnalyticsDataIsLoopbackOnly(t *testing.T) {
	cfg := testConfig(t)
	runtime, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	handler := loopbackOnly(analyticsDataHandler(runtime))

	local := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:27123/analytics/data", nil)
	local.RemoteAddr = "127.0.0.1:54321"
	local.Host = "127.0.0.1:27123"
	localRecorder := httptest.NewRecorder()
	handler.ServeHTTP(localRecorder, local)
	if localRecorder.Code != http.StatusOK {
		t.Fatalf("local status = %d, want 200; body=%s", localRecorder.Code, localRecorder.Body.String())
	}
	if !strings.Contains(localRecorder.Body.String(), `"recent_capacity":500`) {
		t.Fatalf("analytics data missing capacity: %s", localRecorder.Body.String())
	}

	proxied := httptest.NewRequest(http.MethodGet, "https://public.example/analytics/data", nil)
	proxied.RemoteAddr = "127.0.0.1:54321"
	proxied.Host = "public.example"
	proxied.Header.Set("CF-Ray", "abc")
	proxyRecorder := httptest.NewRecorder()
	handler.ServeHTTP(proxyRecorder, proxied)
	if proxyRecorder.Code != http.StatusForbidden {
		t.Fatalf("proxied status = %d, want 403", proxyRecorder.Code)
	}
}
