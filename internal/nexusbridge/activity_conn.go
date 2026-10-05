package nexusbridge

import (
	"bufio"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// 与 Nexus 拨入侧相同。局域网 HTTP 没有 WriteTimeout；这里按本次写入大小延长底限，上限 5 分钟。
	tailcatWriteChunk = 64 << 10
	tailcatWriteSlice = time.Second
	tailcatWriteMax   = 5 * time.Minute
)

// tailcatActivityConn 只包住 Tailcat 入站节点 WebSocket 劫持到的 TCP。
// 读侧按最近一次空闲窗口在每个数据块上重新计时，连续约两个心跳没有字节仍结束会话。
// 写侧让 16MiB 的工具结果能写完。出站 Device Token 连接不经过这层。
type tailcatActivityConn struct {
	net.Conn
	mu            sync.Mutex
	readIdle      time.Duration
	writeFloor    time.Duration
	writeDeadline time.Time
	// readCalls 让测试确认 Upgrade 之后的帧读取确实经过这层，而不是只包住了连接类型。
	readCalls atomic.Uint32
}

func newTailcatActivityConn(conn net.Conn) net.Conn {
	if conn == nil {
		return nil
	}
	return &tailcatActivityConn{Conn: conn}
}

func (c *tailcatActivityConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *tailcatActivityConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.IsZero() {
		c.readIdle = 0
		return c.Conn.SetReadDeadline(time.Time{})
	}
	idle := time.Until(t)
	if idle < 0 {
		idle = 0
	}
	c.readIdle = idle
	return c.Conn.SetReadDeadline(time.Now().Add(idle))
}

func (c *tailcatActivityConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.IsZero() {
		c.writeFloor = 0
		c.writeDeadline = time.Time{}
		return c.Conn.SetWriteDeadline(time.Time{})
	}
	// 与 Nexus 拨入侧相同。gorilla 会把同一次绝对截止时间套到每个帧上，
	// 按剩余时间重算会在大约 15 秒后把底限收成 0。不晚于已记录截止的调用保持原底限。
	if !c.writeDeadline.IsZero() && !t.After(c.writeDeadline) {
		return nil
	}
	floor := time.Until(t)
	if floor < 0 {
		floor = 0
	}
	c.writeDeadline = t
	c.writeFloor = floor
	return c.Conn.SetWriteDeadline(time.Now().Add(floor))
}

func (c *tailcatActivityConn) Read(p []byte) (int, error) {
	c.readCalls.Add(1)
	c.mu.Lock()
	idle := c.readIdle
	c.mu.Unlock()
	if idle > 0 {
		if err := c.Conn.SetReadDeadline(time.Now().Add(idle)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Read(p)
}

func (c *tailcatActivityConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	floor := c.writeFloor
	c.mu.Unlock()
	if floor > 0 {
		if err := c.Conn.SetWriteDeadline(time.Now().Add(tailcatWriteBudget(floor, len(p)))); err != nil {
			return 0, err
		}
	}
	return c.Conn.Write(p)
}

func tailcatWriteBudget(floor time.Duration, nbytes int) time.Duration {
	chunks := 1
	if nbytes > tailcatWriteChunk {
		chunks = (nbytes + tailcatWriteChunk - 1) / tailcatWriteChunk
	}
	budget := floor + time.Duration(chunks)*tailcatWriteSlice
	if budget > tailcatWriteMax || budget < floor {
		return tailcatWriteMax
	}
	return budget
}

// tailcatActivityWriter 在 Upgrade 劫持时换上进度感知连接。
// gorilla 使用 ResponseController.Hijack，它会选中包装器自己的 Hijack，而不是内层的。
type tailcatActivityWriter struct {
	http.ResponseWriter
}

func (w tailcatActivityWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := hijackUnderlying(w.ResponseWriter)
	if err != nil {
		return nil, nil, err
	}
	return newTailcatActivityConn(conn), rw, nil
}

func (w tailcatActivityWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func hijackUnderlying(w http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	current := w
	for current != nil {
		if hj, ok := current.(http.Hijacker); ok {
			return hj.Hijack()
		}
		unwrapper, ok := current.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		current = unwrapper.Unwrap()
	}
	return nil, nil, http.ErrNotSupported
}
