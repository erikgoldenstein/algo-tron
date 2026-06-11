package main

import (
	"context"
	"log/slog"
	"net"
	"time"
)

func (s *Server) listenTCP(ctx context.Context, addr string, proxyProtocol bool) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	var delay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else if delay < time.Second {
				delay *= 2
			}
			metricTCPAcceptErrors.Inc()
			slog.Warn("tcp accept", "err", err, "retry_in", delay)
			time.Sleep(delay)
			continue
		}
		delay = 0
		go s.handleConn(conn, proxyProtocol)
	}
}
