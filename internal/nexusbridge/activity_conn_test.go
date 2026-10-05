package nexusbridge

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTailcatWriteBudgetCovers16MiB(t *testing.T) {
	if got := tailcatWriteBudget(15*time.Second, 16<<20); got < 4*time.Minute || got >= 5*time.Minute {
		t.Fatalf("16MiB budget = %s", got)
	}
	if got := tailcatWriteBudget(15*time.Second, 64<<20); got != 5*time.Minute {
		t.Fatalf("cap = %s", got)
	}

	left, right := net.Pipe()
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	raw := &recordWriteConn{Conn: left}
	conn := newTailcatActivityConn(raw)
	if err := conn.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(make([]byte, 16<<20)); err != nil {
		t.Fatal(err)
	}
	raw.mu.Lock()
	defer raw.mu.Unlock()
	if len(raw.deadlines) < 2 {
		t.Fatalf("deadlines = %v", raw.deadlines)
	}
	last := raw.deadlines[len(raw.deadlines)-1]
	if last < 4*time.Minute || last > 5*time.Minute {
		t.Fatalf("applied write deadline = %s", last)
	}
}

func TestTailcatActivityConnWriteSurvivesBeyondFloor(t *testing.T) {
	left, right := net.Pipe()
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	go func() {
		buf := make([]byte, 32)
		for {
			if _, err := right.Read(buf); err != nil {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
	}()
	conn := newTailcatActivityConn(left)
	if err := conn.SetWriteDeadline(time.Now().Add(40 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("a"), 32)
	start := time.Now()
	for i := 0; i < 5; i++ {
		if _, err := conn.Write(payload); err != nil {
			t.Fatalf("write %d after %s: %v", i, time.Since(start), err)
		}
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatalf("writes finished too fast: %s", time.Since(start))
	}
}

func TestTailcatActivityConnReadSurvivesGapsInsideIdle(t *testing.T) {
	left, right := net.Pipe()
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	conn := newTailcatActivityConn(left)
	if err := conn.SetReadDeadline(time.Now().Add(80 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	go func() {
		for i := 0; i < 4; i++ {
			time.Sleep(50 * time.Millisecond)
			if _, err := right.Write([]byte("x")); err != nil {
				return
			}
		}
	}()
	buf := make([]byte, 1)
	start := time.Now()
	for i := 0; i < 4; i++ {
		if _, err := conn.Read(buf); err != nil {
			t.Fatalf("read %d after %s: %v", i, time.Since(start), err)
		}
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatalf("transfer finished too fast: %s", time.Since(start))
	}
}

func TestTailcatActivityConnRepeatedDeadlineKeepsWriteFloor(t *testing.T) {
	left, right := net.Pipe()
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	raw := &recordWriteConn{Conn: left}
	conn := newTailcatActivityConn(raw)
	deadline := time.Now().Add(40 * time.Millisecond)
	if err := conn.SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if err := conn.SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(bytes.Repeat([]byte("a"), 32)); err != nil {
		t.Fatal(err)
	}
	raw.mu.Lock()
	defer raw.mu.Unlock()
	if len(raw.deadlines) != 2 {
		t.Fatalf("deadlines = %v", raw.deadlines)
	}
	if raw.deadlines[1] < 900*time.Millisecond || raw.deadlines[1] > 2*time.Second {
		t.Fatalf("extended deadline = %s", raw.deadlines[1])
	}
}

func TestTailcatActivityConnZeroWriteDeadlineClearsFloor(t *testing.T) {
	left, right := net.Pipe()
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	raw := &recordWriteConn{Conn: left}
	conn := newTailcatActivityConn(raw)
	deadline := time.Now().Add(15 * time.Second)
	if err := conn.SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	raw.mu.Lock()
	defer raw.mu.Unlock()
	if len(raw.deadlines) != 2 || raw.deadlines[1] != 0 {
		t.Fatalf("deadlines = %v", raw.deadlines)
	}
}

func TestTailcatActivityConnReadEndsAfterSilence(t *testing.T) {
	left, right := net.Pipe()
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	conn := newTailcatActivityConn(left)
	if err := conn.SetReadDeadline(time.Now().Add(80 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := conn.Read(make([]byte, 1))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("silent read succeeded")
	}
	if elapsed < 60*time.Millisecond || elapsed > time.Second {
		t.Fatalf("silent read took %s: %v", elapsed, err)
	}
	_ = right.Close()
}

func TestInboundUpgradeUsesActivityConn(t *testing.T) {
	readDone := make(chan *tailcatActivityConn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := inboundUpgrader.Upgrade(tailcatActivityWriter{ResponseWriter: w}, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			readDone <- nil
			return
		}
		defer socket.Close()
		activity := activityConnFrom(socket.NetConn())
		var msg map[string]any
		_ = socket.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := socket.ReadJSON(&msg); err != nil {
			t.Errorf("read: %v", err)
		}
		readDone <- activity
	}))
	t.Cleanup(server.Close)

	socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = socket.Close() })
	if err := socket.WriteJSON(map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	activity := <-readDone
	if activity == nil {
		t.Fatal("upgrade did not wrap the connection")
	}
	// 读缓冲为零时 gorilla 复用劫持前的 Reader，这个计数会停在 0。
	if activity.readCalls.Load() == 0 {
		t.Fatal("frame read bypassed tailcatActivityConn")
	}
}

func activityConnFrom(conn net.Conn) *tailcatActivityConn {
	for conn != nil {
		if activity, ok := conn.(*tailcatActivityConn); ok {
			return activity
		}
		next, ok := conn.(interface{ NetConn() net.Conn })
		if !ok {
			return nil
		}
		unwrapped := next.NetConn()
		if unwrapped == conn {
			return nil
		}
		conn = unwrapped
	}
	return nil
}

type recordWriteConn struct {
	net.Conn
	mu        sync.Mutex
	deadlines []time.Duration
}

func (c *recordWriteConn) Write(p []byte) (int, error) { return len(p), nil }

func (c *recordWriteConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.IsZero() {
		c.deadlines = append(c.deadlines, 0)
		return nil
	}
	c.deadlines = append(c.deadlines, time.Until(t))
	return nil
}

func (c *recordWriteConn) Read(p []byte) (int, error) { return 0, io.EOF }
