# Persistence

The server keeps state in a single directory configured with `-data-dir`. Default is `${TMPDIR}/algo-tron` — fine for local dev, **not for production**: set `-data-dir` to a persistent path or use the NixOS module (which defaults to `/var/lib/algo-tron`).

## Layout

```
<data-dir>/
├── secret                    # 32 raw bytes, 0600
└── players.db                # SQLite, modernc.org/sqlite
```

The GeoLite2 `.mmdb` files live in a separate directory configured with `-geo-dir` (default `geo` relative to cwd; the NixOS module uses `/var/lib/algo-tron/geo`). It is not under `-data-dir` — geo data is read-only enrichment, not server state.

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
  score_history TEXT NOT NULL DEFAULT '[]', -- JSON: [{type:1|0, time: unix_ms, elo?: float, tsMu?: float, tsSigma?: float}, …]
  ts_mu          REAL NOT NULL DEFAULT 0,    -- TrueSkill mean; 0 = uninitialized
  ts_sigma       REAL NOT NULL DEFAULT 0,    -- TrueSkill uncertainty; 0 = uninitialized
  last_seen_unix INTEGER NOT NULL DEFAULT 0, -- last join/disconnect; drives idle takeover + pruning
  uuid           TEXT NOT NULL DEFAULT ''    -- stable per-career identity
);
CREATE UNIQUE INDEX IF NOT EXISTS players_uuid_idx ON players(uuid) WHERE uuid <> '';

CREATE TABLE IF NOT EXISTS players_archive (
  username         TEXT NOT NULL,   -- same username can appear once per retirement
  pw_hash          TEXT NOT NULL,
  elo              REAL NOT NULL,
  score_history    TEXT NOT NULL,
  ts_mu            REAL NOT NULL,
  ts_sigma         REAL NOT NULL,
  last_seen_unix   INTEGER NOT NULL,
  archived_at_unix INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS game_participants (
  game_id       TEXT NOT NULL,
  board_index   INTEGER NOT NULL,
  uuid          TEXT NOT NULL,
  username      TEXT NOT NULL, -- display name at game end
  won           INTEGER NOT NULL, -- 1 for winners, 0 otherwise; winners derive from this
  death_reason  TEXT NOT NULL,
  elo           REAL NOT NULL,
  ts_mu         REAL NOT NULL,
  ts_sigma      REAL NOT NULL,
  ended_unix_ms INTEGER NOT NULL,
  tick_count    INTEGER NOT NULL DEFAULT 0 -- total ticks the game lasted
);
CREATE INDEX IF NOT EXISTS game_participants_uuid_ended_idx ON game_participants(uuid, ended_unix_ms);
CREATE INDEX IF NOT EXISTS game_participants_ended_idx ON game_participants(ended_unix_ms);

-- game_participants_archive: identical columns to game_participants. Aged-out
-- ledger rows (older than gameLedgerRetention, ~7 months) are moved here by
-- archiveOldGameParticipants so the hot table stays bounded; kept for history,
-- never queried by the server.

CREATE TABLE IF NOT EXISTS player_ips (
  uuid            TEXT NOT NULL,
  ip_hash         TEXT NOT NULL, -- HMAC-SHA256(secret-derived-key, canonical IP)
  family          TEXT NOT NULL, -- ipv4 / ipv6 / unknown
  country         TEXT NOT NULL DEFAULT '',
  region          TEXT NOT NULL DEFAULT '',
  city            TEXT NOT NULL DEFAULT '',
  asn             INTEGER NOT NULL DEFAULT 0,
  as_org          TEXT NOT NULL DEFAULT '',
  as_type         TEXT NOT NULL DEFAULT '',
  first_seen_unix INTEGER NOT NULL,
  last_seen_unix  INTEGER NOT NULL,
  PRIMARY KEY (uuid, ip_hash)
);
```

The DB runs in WAL mode with a 5s busy timeout (set best-effort on every open).

- `pw_hash` is hex-encoded HMAC-SHA256 of the password with `secret` as key.
- `elo` defaults to 1000 for new players; rows with `elo == 0` from legacy data are upgraded to 1000 on load.
- `score_history` is a JSON array of `Score` records. `type` is `1` for wins, `0` for losses. `elo`, `tsMu`, and `tsSigma` are the player's ratings after that game; all three are `omitempty` for backward compatibility, so records written before a given metric existed lack the field and parse as `0`. The viewer's TrueSkill chart skips slots with `TsMu == 0` (see [game-mechanics.md § Scoreboard](game-mechanics.md#scoreboard)). Never pruned on disk — the in-memory copy is the one that's trimmed to `scoreWindow` (see [game-mechanics.md](game-mechanics.md)).
- `ts_mu` / `ts_sigma` are added by idempotent `ALTER TABLE` on open so existing databases pick up the columns. A row with `ts_sigma == 0` is treated as "no rating yet" and gets initialized to `(tsMu0, tsSigma0)` the next time the player plays a game (see [game-mechanics.md](game-mechanics.md)).
- `uuid` is the stable identity for persistence/audit rows. Usernames remain the login/display lookup; idle takeover after 30 days archives the old career and gives the username a new UUID.
- `game_participants` is the single ledger of played games: one row per human participant per game, with `game_id` (timestamped game), `ended_unix_ms`, `uuid`, `username` at the time, `tick_count` (how long the game lasted), and `won=1` for the survivors. To reconstruct "who won game X" run `SELECT uuid FROM game_participants WHERE game_id = ? AND won = 1`; a separate winners table is intentionally not kept (it would duplicate this row set — a legacy `game_winners` table is dropped on open if present). Internal filler bots and other non-leaderboard accounts are excluded at write time so the period boards and the audit log agree.
- `game_participants_archive` holds ledger rows aged out past `gameLedgerRetention` (~7 months, `scoreboard_config.go`), moved there by `archiveOldGameParticipants` so the hot table and its indexes stay bounded by the longest live board window. Same columns as `game_participants`; kept for history, never read by the server.
- `player_ips` never stores raw IPs. It stores a secret-keyed hash plus optional GeoLite2 City/ASN enrichment. `as_type` is a simple local classification from AS organization names (`datacenter`, `university`, `residential`, `business`, or empty).

### GeoLite setup

Run `algo-tron -setup-geo -geo-dir geo` to ensure `GeoLite2-City.mmdb` and `GeoLite2-ASN.mmdb` exist in `-geo-dir` (default `geo`). Normal server startup only opens existing files; it does not download over the network. The setup command mirrors the common GeoLite build-script environment:

- `SKIP_BUILD_GEO=1` skips geo setup entirely.
- `VERCEL=1` skips unless `BUILD_GEO=1` is also set.
- `GEO_DATABASE_URL` can point to a City `.mmdb` or `.tar.gz`.
- `GEO_ASN_DATABASE_URL` can point to an ASN `.mmdb` or `.tar.gz`.
- `MAXMIND_LICENSE_KEY` downloads from MaxMind when custom URLs are absent.
- Without a license key, it falls back to `GitSquared/node-geolite2-redist` tarballs.

### Read/write cadence

- At boot, `pruneIdleAccounts` first moves accounts idle for more than `accountPruneAfter` (180 days) into `players_archive` and deletes them from `players` — one transaction, so a career is never lost mid-move. Then `s.load()` reads the remaining live rows.
- `players_archive` is the soft-delete side of account recycling: an **idle takeover** (a join with a new password after `accountPasswordResetAfter`, 30 days) snapshots the old career into the archive and resets the live row's stats for the new owner. The server never reads the archive; it exists so history survives on disk (`sqlite3 players.db "SELECT * FROM players_archive"`).
- Writes are asynchronous: every game end signals the persister goroutine (`storeLoop`), which snapshots the **dirty players** (those whose ratings/history/account changed since the last store — see `Server.dirty`) under the lock, then opens a transaction and `INSERT OR REPLACE`s those rows with **no lock held** — disk latency never delays a game tick. The signal channel has capacity 1; back-to-back game ends coalesce into one write covering all accumulated dirty players. If the transaction fails, the players are re-marked dirty so the next store retries them.
- On shutdown, `main` runs one final synchronous `s.store()` after the listeners exit — it writes **all** players, not just dirty ones, so a missed dirty mark costs freshness, never data.
- One accepted staleness: `trimScores` rewrites in-memory `ScoreHistory` during scoreboard rebuilds without marking players dirty, so an inactive player's DB row keeps expired score entries until their next game (or shutdown). Harmless — the trim re-applies in memory after every load. Don't "fix" it by marking everyone dirty per rebuild; that reintroduces the full-table write this design removes.

DB errors are logged and counted as `tron_db_errors_total{op="…"}`; the server keeps running. There is no migration system — the schema only ever gets new columns by direct ALTER (TrueSkill is the first such case; `ts_mu` / `ts_sigma` are added via `ALTER TABLE players ADD COLUMN … DEFAULT 0` on every `openDB` and the duplicate-column error is intentionally swallowed).

## Logs

The server writes slog text-handler output to stderr. Persistence and rotation are the operator's job — under the NixOS module this means journald (`journalctl -u algo-tron`).
