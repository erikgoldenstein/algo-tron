.DEFAULT_GOAL := help

BINARY ?= ./bin/algo-tron
SERVER_ARGS ?=
BOT_ARGS ?=
PYTHON ?= python3

.PHONY: help build run run-server dev run-bot stop-bots dry-run test check clean

help:
	@printf '%s\n' \
		'make build                         Build the Go server' \
		'make run                           Build and run the Go server' \
		'make dev                           Auto-restart server on source changes' \
		'make run-bot                       Run the default 64-bot swarm' \
		'make run-bot BOT_ARGS="--lobby mrmcd --prefix mrmcd-swarm --pid-file scripts/.tron-swarm-mrmcd.pid"' \
		'make stop-bots                     Stop the swarm' \
		'make dry-run BOT_ARGS="--seed 7"  Print bot profiles without connecting' \
		'make test                          Run Go tests' \
		'make check                         Run tests and Python syntax checks' \
		'' \
		'Pass server flags with SERVER_ARGS, bot flags with BOT_ARGS.'

build:
	@mkdir -p $(dir $(BINARY))
	go build -o $(BINARY) ./cmd/algo-tron

run: build
	$(BINARY) $(SERVER_ARGS)

run-server: run

dev:
	$(PYTHON) scripts/dev.py -- $(SERVER_ARGS)

run-bot:
	$(PYTHON) scripts/bot_swarm.py $(BOT_ARGS)

stop-bots:
	$(PYTHON) scripts/bot_swarm.py --stop $(BOT_ARGS)

dry-run:
	$(PYTHON) scripts/bot_swarm.py --dry-run $(BOT_ARGS)

test:
	go test ./...

check: test
	$(PYTHON) -m py_compile scripts/bot_swarm.py scripts/bot_swarm/*.py

clean:
	$(RM) $(BINARY)
