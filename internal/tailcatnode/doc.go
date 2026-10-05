// Package tailcatnode 在本进程里运行一台 Tailcat 服务器。
// 同一个 TCP 端口提供节点 WebSocket，以及编码代理直接拨入时使用的 /mcp。
//
// 只有 runtime.go 可以导入 Tailcat 库。
// 控制面、安装器和 Nexus 桥只使用本包导出的配置与状态。连接串按密码保管，不能进日志。
package tailcatnode
