package httpx

import "encoding/json"

const (
	mcpProtocolVersionHeader   = "Mcp-Protocol-Version"
	mcpProtocolVersion20260728 = "2026-07-28"
	mcpMetaProtocolVersion     = "io.modelcontextprotocol/protocolVersion"
	mcpMetaClientCapabilities  = "io.modelcontextprotocol/clientCapabilities"
)

// repairCancelNotification 补上官方 MCP 客户端漏发的协议字段。
// 取消 tools/call 时，客户端会再 POST 一条 notifications/cancelled，但这条通知不走
// 请求的 _meta 注入。2026-07-28 服务端因此返回 400，客户端把这次写入当成连接损坏，
// 同一条会话上的后续调用全部失败。工具本身已经随原 POST 的取消停掉，这里只让这条
// 通知被接受，避免会话被连带关掉。
func repairCancelNotification(body []byte, protocolVersion string) []byte {
	if protocolVersion < mcpProtocolVersion20260728 {
		return body
	}
	var message map[string]any
	if err := json.Unmarshal(body, &message); err != nil {
		return body
	}
	if message["method"] != "notifications/cancelled" {
		return body
	}
	if _, hasID := message["id"]; hasID {
		return body
	}
	params, _ := message["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
		message["params"] = params
	}
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		params["_meta"] = meta
	}
	if _, ok := meta[mcpMetaProtocolVersion]; ok {
		return body
	}
	meta[mcpMetaProtocolVersion] = protocolVersion
	if _, ok := meta[mcpMetaClientCapabilities]; !ok {
		meta[mcpMetaClientCapabilities] = map[string]any{}
	}
	repaired, err := json.Marshal(message)
	if err != nil {
		return body
	}
	return repaired
}
