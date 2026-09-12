# Example bots

Three small Python bots that show the complete basic client:

- `bot1_random.py` — chooses a random free direction.
- `bot2_bfs_depth8.py` — chooses the direction with the most nearby space.
- `bot3_adaptive_bfs.py` — searches around all players and adapts to the tick rate.

They use only Python's standard library. Start the server, then run one:

```sh
python3 bot1_random.py tron.erik.gdn 4000 mybot v1
```

The arguments are optional and mean `host port username version`. The password
is `secret` in the example code; change it in the bot if needed.

## please use VERSIONS ;)
`username` + `password` is one account. The optional version string identifies
an independent bot career under that account. This is intended for running
multiple bots from one account: use the same username and password with a
different version for each bot.

```sh
python3 bot1_random.py 127.0.0.1 4000 myaccount random
python3 bot2_bfs_depth8.py 127.0.0.1 4000 myaccount bfs8
python3 bot3_adaptive_bfs.py 127.0.0.1 4000 myaccount adaptive
```

Each version has separate ratings, history, and leaderboard row. Version
strings may contain letters, numbers, `.`, `_`, and `-`, and may be at most
8 characters. If omitted, the version is empty and not shown. Reusing the same version while
the first bot is connected replaces the first connection. Choose a normal
username: `online` and `bot...` names are reserved outside localhost.

## Protocol in one minute

The protocol is plain TCP text: one UTF-8 packet per line, with fields separated
by `|`.

```text
join|username|password|version
move|up
```

After joining, the server sends a `game` snapshot, then repeats `pos` and
`tick`. Send one `move` after each `tick`. The board wraps at its edges, and
the `pos` packets plus the `game`/`player` packets are enough to maintain the
state used by these examples. A game ends with `win` or `lose`; wait for the
next `game` packet and keep running.

The client also supports the optional packets:

```text
lobby|workshop
lobby|workshop|password
chat|hello
bio|contact|you@example.com
bio|src|https://git.example.com/you/my-bot
```

`lobby` changes the queue used after the current game, so changing it during a
game does not move the bot immediately. `src` may be any printable source
address or text, not only GitHub. `chat` is limited to one posted message per
tick interval. Values cannot contain `|`.

Send at most a few packets per tick and normally only one `move`; sustained
packet spam is rate-limited and can disconnect the account. If a move is
missing or invalid, the server chooses a safe fallback for the first two
consecutive misses, then disconnects the bot. The client reconnects after a
connection loss; a reconnect can resume a still-live seat, but old trail cells
are not replayed.

For all packet types, limits, errors, reconnect behavior, and exact packet
formats, see the [full bot protocol](../docs/bot-protocol.md). The shared
client is in [`client.py`](client.py); `decide(client)` is the only function a
new strategy needs to implement.

## Running locally

From this directory:

```sh
python3 bot1_random.py
```

The default is `127.0.0.1:4000`. No dependencies beyond CPython 3.11+ are
required.
