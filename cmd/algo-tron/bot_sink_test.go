package main

import (
	"testing"
	"time"
)

// The writer must deliver queued packets and close the connection after
// shutdown — a kicked or disconnecting bot still gets its final error
// packet (best-effort, bounded by botWriteTimeout).
func TestBotSinkDrainsOnShutdown(t *testing.T) {
	clientConn, serverConn := mustPipe(t)
	sink := newBotSink(serverConn)
	sink.enqueue([]byte("error|ERROR_RATE_LIMIT\n"))
	sink.shutdown()
	go sink.run()

	buf := make([]byte, 64)
	n, err := clientConn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "error|ERROR_RATE_LIMIT\n" {
		t.Errorf("client received %q, want the queued error packet", got)
	}
	// After the drain the connection must be closed.
	clientConn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := clientConn.Read(buf); err == nil {
		t.Error("connection should be closed after drain")
	}
}

// A full sink must kick (shutdown) instead of blocking the sender.
func TestBotSinkKicksWhenFull(t *testing.T) {
	_, serverConn := mustPipe(t)
	sink := newBotSink(serverConn) // no writer goroutine — nothing drains
	for i := 0; i < botSinkBuf; i++ {
		sink.enqueue([]byte("pos|0|0|0\n"))
	}
	sink.enqueue([]byte("one too many\n")) // must not block

	select {
	case <-sink.done:
	default:
		t.Error("overflowing the sink must shut it down")
	}
	if !sink.kicked.Load() {
		t.Error("overflow must mark the sink kicked")
	}
}
