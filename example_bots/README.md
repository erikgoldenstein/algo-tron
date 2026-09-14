# Example bots

Three small Python bots that show the complete basic client:

- `bot1_random.py`: chooses a random free direction.
- `bot2_bfs_depth8.py`: chooses the direction with the most nearby space.
- `bot3_adaptive_bfs.py`: compares reachable space against opponents within a tick-time budget.

## Run your first bot

You need Python 3.11 or newer, with no additional packages. From this directory, connect an example to the public server:

```sh
export TRON_PASSWORD='choose-a-password-only-for-this-game'
python3 bot1_random.py tron.erik.gdn 4000 mybot v1
```

The positional arguments are optional and mean `host port username version`.
Credentials come from `TRON_PASSWORD`, not from the strategy source. Use your
host's secret/environment settings for unattended runs. Without a password,
the bot uses a temporary session; omit the version in that case. See
[accounts](../docs/accounts.md) for persistence and version behavior.

The default names (`bot1`, `bot2`, `bot3`) are for localhost testing. Supply
your own username when connecting to a remote server.

## Writing a strategy

The shared [`client.py`](client.py) handles the connection and tracks board state. Implement `decide(client)` to add a strategy; use the existing bots as templates. Construction does not open a socket; `run(decide)` connects. For the wire lifecycle and state reconstruction, see the [bot protocol](../docs/bot-protocol.md#connection-lifecycle), and for movement and collisions see [game mechanics](../docs/game-mechanics.md).

Keep decisions short. Bot3 uses half the estimated tick interval, checks its
deadline during frontier expansion, and accepts only fully evaluated search
rounds. This is a cooperative time budget, not a hard real-time guarantee;
its opponent model compares reachable cells rather than predicting exact moves.

## Running multiple strategies

Use one account with a different version for each strategy; [accounts and versions](../docs/accounts.md#independent-versions) explains career ownership and connection replacement.

```sh
export TRON_PASSWORD='choose-a-password-only-for-this-game'
python3 bot1_random.py tron.erik.gdn 4000 myaccount random
python3 bot2_bfs_depth8.py tron.erik.gdn 4000 myaccount bfs8
python3 bot3_adaptive_bfs.py tron.erik.gdn 4000 myaccount adaptive
```

## Recovery behavior

Initial connection failures and later socket errors retry with delays from
one to 30 seconds. A received tick resets the backoff. The client processes
already-arrived frames before choosing a move, so buffered ticks do not each
trigger an outdated decision. Board and strategy timing state reset for each
new game snapshot.

Invalid credentials, connection replacement, invalid-move kicks, and
rate-limit kicks/penalties stop the client with a printed error. Fix the
configuration or strategy before restarting; replacement must not cause two
copies of the same career to repeatedly reconnect and kick each other.
Warnings and optional-packet validation errors are logged without stopping.
Strategy exceptions remain visible and close the socket rather than being
silently retried.

After reconnecting, the client replays its configured lobby and metadata.
If a removed lobby is rejected, the server's fallback selection applies;
the client keeps the requested lobby for a future reconnect. A mid-game
resync still cannot recover old trails because the protocol does not replay
them; that game's obstacle map will be incomplete.

## Lobby and profile configuration

Use `--lobby workshop` or `TRON_LOBBY` to choose a lobby. Set
`TRON_LOBBY_PASSWORD` for a protected lobby, and optionally set `TRON_CONTACT`
and `TRON_SRC` to publish profile information. For example:

```sh
python3 bot2_bfs_depth8.py tron.erik.gdn 4000 myaccount bfs8 --lobby workshop
```

Custom strategies can also call `send_lobby()` and `send_bio()` on the client
before `run()` to configure values, or during play to send updates. These
settings are retained for reconnects. `send_chat()` sends only on the current
connection and is not replayed.

### Optional packets

The client also supports the optional packets:

```text
lobby|workshop
lobby|workshop|password
chat|hello
bio|contact|you@example.com
bio|src|https://git.example.com/you/my-bot
```

See the protocol sections for [lobby selection](../docs/bot-protocol.md#lobby-selection), [chat](../docs/bot-protocol.md#chat), and [contact/source metadata](../docs/bot-protocol.md#profile-metadata) for syntax, limits, and effects.

## Running locally

If you already have a local game server, run from this directory:

```sh
python3 bot1_random.py
```

The default is `127.0.0.1:4000`. No dependencies beyond CPython 3.11+ are
required.

## Keep your bot online

Once your strategy runs reliably, see [hosting a bot](hosting.md) for running it on an always-on machine or a hosted service.
