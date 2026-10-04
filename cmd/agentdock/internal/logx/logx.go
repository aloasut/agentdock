package logx

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"github.com/uvwt/agentdock/internal/secretredact"
)

func Setup(level string, output io.Writer) {
	var slogLevel slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}
	// 输出目标由启动链路决定：普通进程继续走 stderr；桌面后台进程可传入轮转文件。
	handler := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slogLevel})
	slog.SetDefault(slog.New(redactingHandler{next: handler}))
}

// redactingHandler 拦住 Tailcat 连接串、psk 和 privkey，避免上游库或错误文本把它们打进日志。
type redactingHandler struct {
	next slog.Handler
}

func (h redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	cleaned := slog.NewRecord(record.Time, record.Level, secretredact.Text(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		cleaned.AddAttrs(redactAttr(attr))
		return true
	})
	return h.next.Handle(ctx, cleaned)
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		cleaned[i] = redactAttr(attr)
	}
	return redactingHandler{next: h.next.WithAttrs(cleaned)}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{next: h.next.WithGroup(name)}
}

func redactAttr(attr slog.Attr) slog.Attr {
	attr.Value = redactValue(attr.Value)
	return attr
}

func redactValue(value slog.Value) slog.Value {
	switch value.Kind() {
	case slog.KindString:
		return slog.StringValue(secretredact.Text(value.String()))
	case slog.KindAny:
		if text, ok := value.Any().(string); ok {
			return slog.StringValue(secretredact.Text(text))
		}
		if err, ok := value.Any().(error); ok && err != nil {
			return slog.StringValue(secretredact.Text(err.Error()))
		}
	case slog.KindGroup:
		attrs := value.Group()
		cleaned := make([]slog.Attr, len(attrs))
		for i, attr := range attrs {
			cleaned[i] = redactAttr(attr)
		}
		return slog.GroupValue(cleaned...)
	case slog.KindTime:
		return slog.TimeValue(value.Time())
	case slog.KindDuration:
		return slog.DurationValue(value.Duration())
	}
	return value
}
