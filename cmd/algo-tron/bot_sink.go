package main

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// botSink is the per-bot outbound queue, mirroring viewerSink on the TCP
// side. All packets to a bot are enqueued (never written inline), so no
// goroutine ever blocks on a bot's socket while holding a lock — a stalled
// client can no longer delay a tick. A dedicated writer goroutine (run)
// drains ch and writes with a deadline; if the buffer fills, the bot is too
// slow and gets kicked, exactly like a slow viewer.
//
// ch is never closed (closing would race with concurrent enqueues). done is
// closed exactly once via shutdown() or shutdownWithPacket(); the writer then
// flushes the final packet or whatever is still queued — bounded by one
// botWriteTimeout overall — and closes the connection. The connection reader
// noticing the close performs the player-state cleanup in handleConn.
type botSink struct {
	conn   net.Conn
	ch     chan []byte
	done   chan struct{}
	once   sync.Once
	kicked atomic.Bool
	reason atomic.Pointer[string]
	final  atomic.Pointer[[]byte]
}

func newBotSink(conn net.Conn) *botSink {
	return &botSink{conn: conn, ch: make(chan []byte, botSinkBuf), done: make(chan struct{})}
}

func (b *botSink) shutdown(reasons ...string) {
	if len(reasons) > 0 {
		b.setCloseReason(reasons[0])
	}
	b.once.Do(func() { close(b.done) })
}

// shutdownWithPacket sends one final control packet before closing, dropping
// stale queued game frames. Used for server shutdown so the restart notice is
// not delayed behind a slow bot's backlog.
func (b *botSink) shutdownWithPacket(packet []byte, reason string) {
	b.setCloseReason(reason)
	b.final.Store(&packet)
	b.once.Do(func() { close(b.done) })
}

func (b *botSink) setCloseReason(reason string) {
	if reason == "" {
		return
	}
	b.reason.CompareAndSwap(nil, &reason)
}

func (b *botSink) closeReason() string {
	if reason := b.reason.Load(); reason != nil {
		return *reason
	}
	return ""
}

// enqueue queues one packet for the writer. Callers may hold any lock —
// this never blocks. A full buffer means the bot has fallen botSinkBuf
// packets behind; it gets kicked (connection closed after a best-effort
// flush) rather than ever stalling the sender.
func (b *botSink) enqueue(data []byte) {
	select {
	case b.ch <- data:
	default:
		if b.kicked.CompareAndSwap(false, true) {
			metricBotsKicked.Inc()
		}
		b.shutdown("send_buffer_full")
	}
}

// run is the writer goroutine: one per bot connection. It owns all writes
// to conn after the join handshake and is the only place that closes conn
// on the write side.
func (b *botSink) run() {
	for {
		select {
		case <-b.done:
			b.finishShutdown()
			return
		case data := <-b.ch:
			// A shutdown packet takes priority over any queued game frame.
			if b.final.Load() != nil {
				b.finishShutdown()
				return
			}
			start := time.Now()
			b.conn.SetWriteDeadline(start.Add(botWriteTimeout))
			_, err := b.conn.Write(data)
			metricBotWrite.Observe(time.Since(start).Seconds())
			if err != nil {
				b.setCloseReason("write_error")
				b.conn.Close()
				return
			}
		}
	}
}

func (b *botSink) finishShutdown() {
	b.conn.SetWriteDeadline(time.Now().Add(botWriteTimeout))
	if packet := b.final.Load(); packet != nil {
		if _, err := b.conn.Write(*packet); err != nil {
			b.setCloseReason("write_error")
		}
		b.conn.Close()
		return
	}
	// Normal kicks still drain their queued final error packet(s).
	for {
		select {
		case data := <-b.ch:
			if _, err := b.conn.Write(data); err != nil {
				b.setCloseReason("write_error")
				b.conn.Close()
				return
			}
		default:
			b.conn.Close()
			return
		}
	}
}
