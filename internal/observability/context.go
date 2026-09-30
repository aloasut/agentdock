package observability

import "context"

// Source 标识一次工具调用进入 Runtime 的传输来源。
// 来源只允许固定枚举，避免把任意请求数据带入观测记录。
type Source string

const (
	SourceInternal Source = "internal"
	SourceMCP      Source = "mcp"
	SourceNexus    Source = "nexus"
)

type sourceContextKey struct{}

func WithSource(ctx context.Context, source Source) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	switch source {
	case SourceMCP, SourceNexus, SourceInternal:
	default:
		source = SourceInternal
	}
	return context.WithValue(ctx, sourceContextKey{}, source)
}

func SourceFromContext(ctx context.Context) Source {
	if ctx == nil {
		return SourceInternal
	}
	source, ok := ctx.Value(sourceContextKey{}).(Source)
	if !ok {
		return SourceInternal
	}
	switch source {
	case SourceMCP, SourceNexus, SourceInternal:
		return source
	default:
		return SourceInternal
	}
}
