package httpx

import (
	_ "embed"
	"html/template"
	"net/http"
	"strings"

	"github.com/uvwt/agentdock/internal/runtimeapi"
)

//go:embed analytics_page.html
var analyticsPageHTML string

var analyticsTemplate = template.Must(template.New("analytics").Parse(analyticsPageHTML))

type analyticsPageText struct {
	Lang                 string
	Title                string
	Subtitle             string
	LocalOnly            string
	ResourceLoad         string
	HeapMemory           string
	HeapInUse            string
	Goroutines           string
	ActiveCalls          string
	Uptime               string
	CallOverview         string
	TotalCalls           string
	TotalErrors          string
	RecentWindow         string
	ToolStats            string
	StatsHint            string
	Tool                 string
	Calls                string
	ErrorRate            string
	P50                  string
	P95                  string
	P99                  string
	RecentCalls          string
	RecentHint           string
	Time                 string
	Source               string
	Duration             string
	Status               string
	Stages               string
	StageUnit            string
	StageMCPRefresh      string
	StageMCPRemoteCall   string
	StageCommandStart    string
	StageCommandWait     string
	Success              string
	Failed               string
	NoData               string
	LoadFailed           string
	LastUpdated          string
	SourceMCP            string
	SourceNexus          string
	SourceInternal       string
	Milliseconds         string
	Seconds              string
	Minutes              string
	Hours                string
	Days                 string
	WindowSuffix         string
	ErrorCodeUnavailable string
}

var analyticsTextZH = analyticsPageText{
	Lang:                 "zh-CN",
	Title:                "运行分析",
	Subtitle:             "查看工具调用耗时、错误统计与 AgentDock 运行资源。",
	LocalOnly:            "仅本机",
	ResourceLoad:         "资源与负载",
	HeapMemory:           "Go 堆内存",
	HeapInUse:            "使用中",
	Goroutines:           "Goroutine",
	ActiveCalls:          "进行中调用",
	Uptime:               "本次运行",
	CallOverview:         "调用概览",
	TotalCalls:           "总调用",
	TotalErrors:          "失败调用",
	RecentWindow:         "统计窗口",
	ToolStats:            "调用统计",
	StatsHint:            "P50 表示典型耗时；P95 / P99 用于观察少数慢调用。统计基于当前保留的最近调用。",
	Tool:                 "工具",
	Calls:                "调用数",
	ErrorRate:            "错误率",
	P50:                  "典型耗时 · P50",
	P95:                  "较慢调用 · P95",
	P99:                  "尾部耗时 · P99",
	RecentCalls:          "调用记录",
	RecentHint:           "仅保存工具名、来源、耗时、阶段、状态与错误码；不保存参数、结果、命令或文件内容。",
	Time:                 "时间",
	Source:               "来源",
	Duration:             "耗时",
	Status:               "状态",
	Stages:               "阶段",
	StageUnit:            "段",
	StageMCPRefresh:      "MCP 初始化与发现",
	StageMCPRemoteCall:   "MCP 远端调用",
	StageCommandStart:    "命令启动",
	StageCommandWait:     "前台等待",
	Success:              "正常",
	Failed:               "失败",
	NoData:               "暂无调用数据",
	LoadFailed:           "无法读取运行分析数据",
	LastUpdated:          "更新于",
	SourceMCP:            "MCP",
	SourceNexus:          "Nexus",
	SourceInternal:       "内部",
	Milliseconds:         "毫秒",
	Seconds:              "秒",
	Minutes:              "分钟",
	Hours:                "小时",
	Days:                 "天",
	WindowSuffix:         "次",
	ErrorCodeUnavailable: "未分类错误",
}

var analyticsTextEN = analyticsPageText{
	Lang:                 "en",
	Title:                "Runtime Analytics",
	Subtitle:             "Inspect tool latency, error rates, and AgentDock runtime resources.",
	LocalOnly:            "Local only",
	ResourceLoad:         "Resources & Load",
	HeapMemory:           "Go heap",
	HeapInUse:            "In use",
	Goroutines:           "Goroutines",
	ActiveCalls:          "Active calls",
	Uptime:               "Uptime",
	CallOverview:         "Call overview",
	TotalCalls:           "Total calls",
	TotalErrors:          "Failed calls",
	RecentWindow:         "Stats window",
	ToolStats:            "Call statistics",
	StatsHint:            "P50 is typical latency; P95 / P99 expose slower tail calls. Statistics use the currently retained recent-call window.",
	Tool:                 "Tool",
	Calls:                "Calls",
	ErrorRate:            "Error rate",
	P50:                  "Typical · P50",
	P95:                  "Slower · P95",
	P99:                  "Tail · P99",
	RecentCalls:          "Recent calls",
	RecentHint:           "Only tool name, source, latency, stages, status, and error code are retained. Arguments, results, commands, and file contents are never stored.",
	Time:                 "Time",
	Source:               "Source",
	Duration:             "Duration",
	Status:               "Status",
	Stages:               "Stages",
	StageUnit:            "stages",
	StageMCPRefresh:      "MCP initialize & discover",
	StageMCPRemoteCall:   "MCP remote call",
	StageCommandStart:    "Command start",
	StageCommandWait:     "Foreground wait",
	Success:              "OK",
	Failed:               "Failed",
	NoData:               "No call data yet",
	LoadFailed:           "Unable to load runtime analytics",
	LastUpdated:          "Updated",
	SourceMCP:            "MCP",
	SourceNexus:          "Nexus",
	SourceInternal:       "Internal",
	Milliseconds:         "ms",
	Seconds:              "s",
	Minutes:              "min",
	Hours:                "h",
	Days:                 "d",
	WindowSuffix:         "calls",
	ErrorCodeUnavailable: "Unclassified error",
}

func analyticsPageHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		setAnalyticsSecurityHeaders(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := analyticsTemplate.Execute(w, struct{ Text analyticsPageText }{Text: analyticsPageLanguage(r)}); err != nil {
			http.Error(w, "render analytics page failed", http.StatusInternalServerError)
		}
	})
}

func analyticsDataHandler(runtime runtimeapi.Runtime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRuntimeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		setAnalyticsSecurityHeaders(w)
		writeJSON(w, runtime.RuntimeAnalytics())
	})
}

func setAnalyticsSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; img-src 'none'; font-src 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}

func analyticsPageLanguage(r *http.Request) analyticsPageText {
	if r != nil && strings.Contains(strings.ToLower(r.Header.Get("Accept-Language")), "zh") {
		return analyticsTextZH
	}
	return analyticsTextEN
}
