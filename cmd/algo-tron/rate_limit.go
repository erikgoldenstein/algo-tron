package main

import "time"

// tokenBucket is one rate-limit budget: it refills at perTick tokens per
// interval and holds at most rateLimitBurstTicks ticks' worth of tokens, so
// a couple of ticks' budget can arrive in one burst without drops (e.g. a
// client that stalled for a tick and answers two at once). State is owned
// by the connection's reader goroutine — no locking.
type tokenBucket struct {
	tokens float64
	last   time.Time
}

func (tb *tokenBucket) allow(perTick int, interval time.Duration) bool {
	if perTick <= 0 || interval <= 0 {
		return false
	}
	now := time.Now()
	capacity := float64(perTick * rateLimitBurstTicks)
	if tb.last.IsZero() {
		tb.tokens = capacity
	} else {
		tb.tokens += now.Sub(tb.last).Seconds() / interval.Seconds() * float64(perTick)
		if tb.tokens > capacity {
			tb.tokens = capacity
		}
	}
	tb.last = now
	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

// connLimits is the per-connection rate-limit state, local to the reader
// goroutine. It is recreated on every new TCP connection; only the
// cross-connection reconnect penalty lives on Player.
type connLimits struct {
	total, move, chat tokenBucket
	strikes           int
	inDenialRun       bool
	lastStrike        time.Time
}

// allowed marks one within-budget packet: it ends the current denial run,
// so the next over-budget packet earns a fresh strike.
func (lim *connLimits) allowed() {
	lim.inDenialRun = false
}

// handleRateLimit is called on every over-budget packet. A contiguous run
// of denied packets costs one strike (a single burst shouldn't burn through
// all strikes before the client can react to the warning); old strikes are
// forgiven after rateLimitStrikeExpiry of no new ones. It returns false
// when the connection should be closed; on disconnect it also bumps the
// per-player reconnect penalty (doubling, capped at reconnectPenaltyMax)
// which is enforced on the next join attempt. Saved-up penalty decays with
// good behavior — see the redemption block below and
// reconnectPenaltyRedemption in tcp_config.go.
func (s *Server) handleRateLimit(p *Player, lim *connLimits) (bool, string) {
	if lim.inDenialRun {
		return true, "" // this run already cost its strike; just drop
	}
	now := time.Now()
	if lim.strikes > 0 && now.Sub(lim.lastStrike) > rateLimitStrikeExpiry {
		lim.strikes = 0
	}
	lim.inDenialRun = true
	lim.lastStrike = now
	lim.strikes++
	switch {
	case lim.strikes >= rateLimitErrorStrikes:
		s.mu.Lock()
		// Redemption: time spent behaving since the previous ban expired
		// decays the saved-up penalty at 1/reconnectPenaltyRedemption.
		// After reconnectPenaltyRedemption × the previous ban time, the
		// penalty is fully forgiven and the next ban starts at base.
		if p.reconnectPenalty > 0 && reconnectPenaltyRedemption > 0 {
			elapsed := time.Since(p.reconnectAllowedAt)
			if elapsed < 0 {
				elapsed = 0
			}
			decay := elapsed / reconnectPenaltyRedemption
			if decay >= p.reconnectPenalty {
				p.reconnectPenalty = 0
			} else {
				p.reconnectPenalty -= decay
			}
		}
		if p.reconnectPenalty == 0 {
			p.reconnectPenalty = reconnectPenaltyBase
		} else {
			p.reconnectPenalty *= 2
			if p.reconnectPenalty > reconnectPenaltyMax {
				p.reconnectPenalty = reconnectPenaltyMax
			}
		}
		p.reconnectAllowedAt = time.Now().Add(p.reconnectPenalty)
		s.mu.Unlock()
		p.send("error", "ERROR_RATE_LIMIT")
		// Returning false ends the reader loop; its cleanup shuts the
		// sink down, which flushes the error packet and closes.
		return false, "rate_limit"
	default:
		p.send("error", "WARNING_RATE_LIMIT")
		return true, ""
	}
}
