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
  username      TEXT NOT NULL,
  version       TEXT NOT NULL DEFAULT 'v1',
  pw_hash       TEXT NOT NULL,        -- hex(HMAC-SHA256(secret, password))
  elo           REAL NOT NULL DEFAULT 1000,
  score_history TEXT NOT NULL DEFAULT '[]', -- JSON: [{type:1|0, time: unix_ms, elo?: float, tsMu?: float, tsSigma?: float}, …]
  bio            TEXT NOT NULL DEFAULT '{}', -- JSON: {contact?: string, src?: string}
  ts_mu          REAL NOT NULL DEFAULT 0,    -- TrueSkill mean; 0 = uninitialized
  ts_sigma       REAL NOT NULL DEFAULT 0,    -- TrueSkill uncertainty; 0 = uninitialized
  first_seen_unix INTEGER NOT NULL DEFAULT 0, -- first join for this career/UUID
  last_seen_unix INTEGER NOT NULL DEFAULT 0, -- last join/disconnect; drives account recovery/re-registration + pruning
  uuid           TEXT NOT NULL DEFAULT '',   -- stable per-career identity
  PRIMARY KEY (username, version)
);
CREATE UNIQUE INDEX IF NOT EXISTS players_uuid_idx ON players(uuid) WHERE uuid <> '';

CREATE TABLE IF NOT EXISTS players_archive (
  uuid             TEXT NOT NULL DEFAULT '',
  username         TEXT NOT NULL,   -- same username can appear once per retirement
  version          TEXT NOT NULL DEFAULT 'v1',
  pw_hash          TEXT NOT NULL,
  elo              REAL NOT NULL,
  score_history    TEXT NOT NULL,
  bio              TEXT NOT NULL,       -- JSON: {contact?: string, src?: string}
  ts_mu            REAL NOT NULL,
  ts_sigma         REAL NOT NULL,
  first_seen_unix  INTEGER NOT NULL,
  last_seen_unix   INTEGER NOT NULL,
  archived_at_unix INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS lobbies (
  name                   TEXT NOT NULL PRIMARY KEY,
  password_hash          TEXT NOT NULL DEFAULT '',
  max_players_per_board INTEGER NOT NULL DEFAULT 24,
  created_unix           INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS game_participants (
  game_id       TEXT NOT NULL,
  board_index   INTEGER NOT NULL,
  lobby         TEXT NOT NULL DEFAULT 'default',
  uuid          TEXT NOT NULL,
  username      TEXT NOT NULL, -- display name at game end
  version       TEXT NOT NULL DEFAULT 'v1',
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
-- archiveOldGameParticipants so the hot table stays bounded; the history API
-- may read it for seven-day windows, and rows older than 14 months are pruned.

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
- `score_history` is a JSON array of `Score` records. `type` is `1` for wins, `0` for losses. `elo`, `tsMu`, and `tsSigma` are the player's ratings after that game; all three are `omitempty` for backward compatibility, so records written before a given metric existed lack the field and parse as `0`. The viewer's TrueSkill chart skips slots with `TsMu == 0` (see [game-mechanics.md § Scoreboard](game-mechanics.md#scoreboard)). Normal score-window trimming happens in memory; a separate retention sweep permanently removes records older than 14 months from disk.
- `ts_mu` / `ts_sigma` are added by idempotent `ALTER TABLE` on open so existing databases pick up the columns. A row with `ts_sigma == 0` is treated as "no rating yet" and gets initialized to `(tsMu0, tsSigma0)` the next time the player plays a game (see [game-mechanics.md](game-mechanics.md)).
- `uuid` is the stable identity for persistence rows. `first_seen_unix` records when that career/UUID was first created; `last_seen_unix` records the most recent join or disconnect. Usernames remain the login/display lookup; account recovery/re-registration after 14 months of inactivity purges the old career and gives the username/version a new UUID and first-seen timestamp. Existing databases without first-seen data are backfilled from their last-seen timestamp, the earliest timestamp available.
- `version` distinguishes independent careers under one username; omitted/legacy values are `v1`. The composite `(username, version)` key allows multiple versions to be online concurrently.
- `lobbies` stores administrator-created lobby names, a keyed password hash, the per-board player limit (`-1` means unlimited for that named lobby), and creation time. The default lobby is implicit and is not stored or removable.
- `bio` stores the optional post-join `contact` and GitHub `src` metadata for that career. It is JSON so absent fields remain absent; validation limits contact to 32 printable ASCII characters and source URLs to 48 characters.
- `game_participants` is the single ledger of played games: one row per human participant per game, with `game_id` (timestamped game), `lobby`, `ended_unix_ms`, `uuid`, `username` and `version` at the time, `tick_count` (how long the game lasted), and `won=1` for the survivors. To reconstruct "who won game X" run `SELECT uuid FROM game_participants WHERE game_id = ? AND won = 1`; a separate winners table is intentionally not kept (it would duplicate this row set — a legacy `game_winners` table is dropped on open if present). Internal filler bots and other non-leaderboard accounts are excluded at write time so the period boards and the audit log agree.
- `game_participants_archive` holds ledger rows aged out past `gameLedgerRetention` (~7 months, `scoreboard_config.go`), moved there by `archiveOldGameParticipants` so the hot table and its indexes stay bounded by the longest live board window. Same columns as `game_participants`; the history API reads both tables. Rows older than 14 months are pruned during retention maintenance. The API's seven-day limit is only a per-request workload bound.
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

- At boot, `pruneIdleAccounts` permanently removes accounts idle for more than `accountPruneAfter` (14 months), including their IP records and legacy archived career rows. The operation is transactional and runs before `s.load()`, so expired accounts do not enter memory.
- A join with a new password is accepted only after `accountPasswordResetAfter` (14 months) of inactivity. Account recovery/re-registration purges the old username's live, archived, IP, and game-ledger rows, then starts a fresh career with a new UUID and first-seen timestamp. This is deliberate: no user account or history data is retained past the 14-month boundary.
- Writes are asynchronous: every game end signals the persister goroutine (`storeLoop`), which snapshots the **dirty players** (those whose ratings/history/account changed since the last store — see `Server.dirty`) under the lock, then opens a transaction and `INSERT OR REPLACE`s those rows with **no lock held** — disk latency never delays a game tick. The signal channel has capacity 1; back-to-back game ends coalesce into one write covering all accumulated dirty players. If the transaction fails, the players are re-marked dirty so the next store retries them.
- On shutdown, `main` runs one final synchronous `s.store()` after the listeners exit — it writes **all** current players, not just dirty ones, so a missed dirty mark costs freshness, never data.
- The regular `trimScores` operation rewrites in-memory `ScoreHistory` during scoreboard rebuilds without marking players dirty; the hourly retention sweep handles the separate 14-month disk-retention boundary and marks changed players dirty.

DB errors are logged and counted as `tron_db_errors_total{op="…"}`; startup now fails when a schema upgrade or required index cannot be applied. Schema additions are idempotent; the pre-version `players` table is rebuilt once to change its primary key to `(username, version)`. Existing first-seen values are preserved, while legacy rows without them use their last-seen timestamp as a fallback. Retention cleanup runs at startup and hourly while the server is running, permanently purging account and game-history data older than 14 months.

## Logs

The server writes slog text-handler output to stderr. Persistence and rotation are the operator's job — under the NixOS module this means journald (`journalctl -u algo-tron`).
