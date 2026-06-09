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
  score_history TEXT NOT NULL DEFAULT '[]'  -- JSON: [{type:1|0, time: unix_ms}, …]
);
```

- `pw_hash` is hex-encoded HMAC-SHA256 of the password with `secret` as key.
- `elo` defaults to 1000 for new players; rows with `elo == 0` from legacy data are upgraded to 1000 on load.
- `score_history` is a JSON array of `Score` records. `type` is `1` for wins, `0` for losses. Never pruned on disk — the in-memory copy is the one that's trimmed to `scoreWindow` (see [game-mechanics.md](game-mechanics.md)).

### Read/write cadence

- `s.load()` runs once at boot — `SELECT username, pw_hash, elo, score_history FROM players`.
- `s.store()` runs once at the end of every game, inside `endLocked`. It opens a transaction and `INSERT OR REPLACE`s every row. No-ops for unchanged rows are fine — there are few players and games are slow.

DB errors are logged and counted as `tron_db_errors_total{op="…"}`; the server keeps running. There is no migration system — the schema only ever gets new columns by direct ALTER (none so far).

## Logs

The server writes slog text-handler output to stderr. Persistence and rotation are the operator's job — under the NixOS module this means journald (`journalctl -u algo-tron`).
