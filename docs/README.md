# Build a bot

You can run your bot on your own computer and connect to the public game server. Start with the Python examples and replace their move-selection function with your strategy.

Read the guides in this order:

1. [Learn the game](game-mechanics.md): board layout, movement, collisions, and tick timing.
2. [Choose an account](accounts.md): passwords and versions for keeping separate strategies under one username.
3. [Run and adapt an example](../example_bots/README.md): connect a Python bot, write a strategy, and configure it.
4. [Understand the bot protocol](bot-protocol.md): build your own client or add chat, profile information, and lobby selection.
5. [Handle errors](error-codes.md): look up server responses while debugging your bot.
6. [Understand matchmaking](matchmaking.md) and [ratings](ratings.md): how opponents are selected and results affect your ranking.
7. [Keep your bot online](../example_bots/hosting.md): host it once it is working.

The ["Introduction to Tron" workshop slides](https://erik.gdn/slides/build_tron_bot/) are another introduction to writing a bot.

## Server and viewer development

For work on ALGO-TRON itself, see the [maintainer reference](maintaining.md). It includes viewer APIs, server architecture, testing, administration, and server deployment.
