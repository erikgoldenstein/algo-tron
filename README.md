<p align="center">
  <img src="assets/logo.png" alt="ALGO-TRON logo" width="240">
</p>

<h1 align="center">ALGO-TRON</h1>

<p align="center">Build a bot, send it into the arena over TCP, and outmaneuver other players' bots in a fast-paced Tron battle.</p>

## The game

- The board is a square that wraps at the edges. Its size scales with the number of players; a board holds at most 32 players, and multiple boards run in parallel.
- Every tick each alive bot must send a move: `up`, `down`, `left`, or `right`.
- You leave a trail behind you. Running into any trail (yours or someone else's) kills you. Two bots arriving at the same cell on the same tick both die.
- Tick rate starts at 1/s and ramps up by +1/s every 10 seconds, so games get faster the longer they last.
- Last bot alive wins. Results feed into a rolling ELO leaderboard and a TrueSkill rating that the [matchmaker](docs/matchmaking.md) uses to put you on a board with similarly skilled bots. When you die you re-enter the queue right away, no waiting for your old game to end.

Full ruleset in [docs/game-mechanics.md](docs/game-mechanics.md).

## Writing a bot

Bots talk a small line-based TCP protocol — no HTTP, no JSON, no SDK. Connect, send your name, read messages, send moves. See [docs/bot-protocol.md](docs/bot-protocol.md) for the wire format.

The fastest way to get started is to read or fork one of the [example bots](example_bots/) (Python). They cover a simple connection lifecycle and a couple of basic strategies.

## Docs

[docs/](docs/README.md) has the protocol spec, error codes, game mechanics, architecture notes, and more.

## Thanks

algo-tron is a Go reimplementation of [freehuntx/gpn-tron](https://github.com/freehuntx/gpn-tron). The bot protocol, the game idea, and the original event around it all come from there. Thanks to the gpn-tron author for the format. This is only possible thanks to the people i met at [gpn24](https://events.ccc.de/en/2026/03/15/gpn24/).
