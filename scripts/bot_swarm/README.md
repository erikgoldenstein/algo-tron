# Local bot swarm

`../bot_swarm.py` starts a self-contained, dependency-free test population.
The default is 64 bots. Every run prints a `profile_seed`; pass it back with
`--seed` to reproduce the same strategy and failure population.

```sh
make run-bot
make run-bot BOT_ARGS='--seed 7 --verbose'
make dry-run BOT_ARGS='--seed 7'
make stop-bots
```

## Profiles

Profiles are assigned with a seeded shuffle. The normal population is mostly
healthy, but the generator guarantees representatives for the important rare
cases in a 64-bot run. Each bot also gets independent strategy parameters and
belongs to a shared network group, so failures can be individual or
correlated.

- `healthy`: small normal latency and occasional natural timing variation.
- `jittery`: variable latency and packet loss.
- `mobile`: higher latency, jitter, pauses, burst loss, and reconnects.
- `slow_cpu`: long decision times, scheduler pauses, perception mistakes, and
  short-lived memory pressure.
- `bad_network`: the roughly-one-percent severe network case: high latency,
  large jitter, loss, outages, and connection failures.
- `protocol`: stale, malformed, duplicate, extra, fragmented, batched, and
  unknown packets, plus occasional invalid metadata.
- `reconnecting`: connection failures, disconnects, simulated crashes, and
  reconnects after games.
- `shared_outage`: a network group occasionally loses connectivity together.
- `chaos`: a moderate combination of several independent faults.

The simulation includes application-level effects of latency, packet loss,
burst loss, batching, fragmentation, bandwidth limits, stale decisions,
malformed moves, duplicate moves, extra moves, slow reads, CPU pauses, clock
skew, incomplete board perception, memory pressure, crashes, reconnects,
invalid joins/bios, oversized packets, unknown packets, chat bursts, and
post-death packets. TCP itself still provides reliable ordered delivery; use
`tc netem` or a network namespace when kernel-level packet behavior is needed.

The swarm is intentionally bounded: simulated memory pressure is short-lived
allocation plus garbage collection, and simulated crashes/disconnects close
only that bot's socket. No host-wide resource exhaustion is attempted.
