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
// closed exactly once via shutdown(); the writer then flushes whatever is
// still queued — bounded by one botWriteTimeout overall — and closes the
// connection. The connection reader noticing the close performs the
// player-state cleanup in handleConn.
type botSink struct {
	conn   net.Conn
	ch     chan []byte
	done   chan struct{}
	once   sync.Once
	kicked atomic.Bool
}

func newBotSink(conn net.Conn) *botSink {
	return &botSink{conn: conn, ch: make(chan []byte, botSinkBuf), done: make(chan struct{})}
}

func (b *botSink) shutdown() { b.once.Do(func() { close(b.done) }) }

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
		b.shutdown()
	}
}

// run is the writer goroutine: one per bot connection. It owns all writes
// to conn after the join handshake and is the only place that closes conn
// on the write side.
func (b *botSink) run() {
	for {
		select {
		case <-b.done:
			// Final drain: deliver what's queued under one shared
			// deadline so a dead peer can't hold this goroutine
			// longer than botWriteTimeout.
			b.conn.SetWriteDeadline(time.Now().Add(botWriteTimeout))
			for {
				select {
				case data := <-b.ch:
					if _, err := b.conn.Write(data); err != nil {
						b.conn.Close()
						return
					}
				default:
					b.conn.Close()
					return
				}
			}
		case data := <-b.ch:
			start := time.Now()
			b.conn.SetWriteDeadline(start.Add(botWriteTimeout))
			_, err := b.conn.Write(data)
			metricBotWrite.Observe(time.Since(start).Seconds())
			if err != nil {
				b.conn.Close()
				return
			}
		}
	}
}
