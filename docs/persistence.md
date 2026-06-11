# Persistence

The server keeps state in a single directory configured with `-data-dir`. Default is `${TMPDIR}/algo-tron` — fine for local dev, **not for production**: set `-data-dir` to a persistent path or use the NixOS module (which defaults to `/var/lib/algo-tron`).

## Layout

```
<data-dir>/
├── secret                    # 32 raw bytes, 0600
└── players.db                # SQLite, modernc.org/sqlite
```

## `secret`

32 bytes from `crypto/rand`, created on first boot. Used as the HMAC-SHA256 key for password hashing. **Rotating it invalidates every account password** — every existing bot will hit `ERROR_WRONG_PASSWORD` and need to re-register under a new name. Don't rotate unless you mean to.

Read at boot; if the file is missing or not 32 bytes a new one is generated and written.

## `players.db`

SQLite, schema created on first open:

```sql
CREATE TABLE IF NOT EXISTS players (
  username      TEXT PRIMARY KEY,
  pw_hash       TEXT NOT NULL,        -- hex(HMAC-SHA256(secret, password))
  elo           REAL NOT NULL DEFAULT 1000,
  score_history TEXT NOT NULL DEFAULT '[]', -- JSON: [{type:1|0, time: unix_ms, elo?: float}, …]
  ts_mu         REAL NOT NULL DEFAULT 0,    -- TrueSkill mean; 0 = uninitialized
  ts_sigma      REAL NOT NULL DEFAULT 0     -- TrueSkill uncertainty; 0 = uninitialized
);
```

- `pw_hash` is hex-encoded HMAC-SHA256 of the password with `secret` as key.
- `elo` defaults to 1000 for new players; rows with `elo == 0` from legacy data are upgraded to 1000 on load.
- `score_history` is a JSON array of `Score` records. `type` is `1` for wins, `0` for losses. `elo` is the player's ELO after that game; it's `omitempty` for backward compatibility, so records written before ELO tracking lack the field and parse as `0`. The viewer's ELO chart simply skips slots with `Elo == 0`. Never pruned on disk — the in-memory copy is the one that's trimmed to `scoreWindow` (see [game-mechanics.md](game-mechanics.md)).
- `ts_mu` / `ts_sigma` are added by idempotent `ALTER TABLE` on open so existing databases pick up the columns. A row with `ts_sigma == 0` is treated as "no rating yet" and gets initialized to `(tsMu0, tsSigma0)` the next time the player plays a game (see [game-mechanics.md](game-mechanics.md)).

### Read/write cadence

- `s.load()` runs once at boot — `SELECT username, pw_hash, elo, score_history, ts_mu, ts_sigma FROM players`.
- Writes are asynchronous: every game end signals the persister goroutine (`storeLoop`), which snapshots all players under the lock, then opens a transaction and `INSERT OR REPLACE`s every row with **no lock held** — disk latency never delays a game tick. The signal channel has capacity 1; back-to-back game ends coalesce into one write of the latest state. `tron_store_seconds` records write durations.
- On shutdown, `main` runs one final synchronous `s.store()` after the listeners exit, so ratings from games that ended since the persister's last run aren't lost. No-ops for unchanged rows are fine — there are few players and games are slow.

DB errors are logged and counted as `tron_db_errors_total{op="…"}`; the server keeps running. There is no migration system — the schema only ever gets new columns by direct ALTER (TrueSkill is the first such case; `ts_mu` / `ts_sigma` are added via `ALTER TABLE players ADD COLUMN … DEFAULT 0` on every `openDB` and the duplicate-column error is intentionally swallowed).

## Logs

The server writes slog text-handler output to stderr. Persistence and rotation are the operator's job — under the NixOS module this means journald (`journalctl -u algo-tron`).
