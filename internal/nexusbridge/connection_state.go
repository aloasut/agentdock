package nexusbridge

import (
	"sync"
	"sync/atomic"
)

// ConnectionState 只记录当前节点桥接是否已完成 NexusDock 握手。
// 配置是否存在由启动配置判断，避免把“已配对”和“已连接”混成同一个状态。
// 出站探测和 Tailcat 入站可以短暂重叠，任一条活会话都算已连接。
type ConnectionState struct {
	mu        sync.Mutex
	sessions  int
	connected atomic.Bool
}

func (s *ConnectionState) SetConnected(connected bool) {
	s.connected.Store(connected)
}

func (s *ConnectionState) Connected() bool {
	if s == nil {
		return false
	}
	return s.connected.Load()
}

// Attach 登记一条已经完成握手的会话。返回的函数只应调用一次。
func (s *ConnectionState) Attach() func() {
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	s.sessions++
	s.connected.Store(true)
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.sessions > 0 {
				s.sessions--
			}
			s.connected.Store(s.sessions > 0)
		})
	}
}
