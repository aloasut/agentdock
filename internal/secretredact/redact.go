// Package secretredact 从日志文本里抹掉 Tailcat 连接串和密钥。
// 传输认证在连接串里，写进日志就等于把隧道交出去。
package secretredact

import "regexp"

// tc 后面至少 16 个 token 字符才算连接串。短得多的 "tc" 前缀留在普通句子里。
var pattern = regexp.MustCompile(`(?:psk:[0-9a-fA-F]+|privkey:[0-9a-fA-F]+|\btc[A-Za-z0-9_-]{16,})`)

// Text 把 psk、privkey 和足够长的 tc 连接串换成占位符。
func Text(value string) string {
	return pattern.ReplaceAllString(value, "[redacted]")
}
