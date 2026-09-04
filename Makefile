.DEFAULT_GOAL := help

BINARY ?= ./bin/algo-tron
SERVER_ARGS ?=
BOT_ARGS ?=
PYTHON ?= python3

.PHONY: help build run run-server run-bot stop-bots dry-run test check clean

help:
	@printf '%s\n' \
		'make build                         Build the Go server' \
		'make run                           Build and run the Go server' \
		'make run-bot                       Run the default 64-bot swarm' \
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

run-bot:
	$(PYTHON) scripts/tron_swarm.py $(BOT_ARGS)

stop-bots:
	$(PYTHON) scripts/tron_swarm.py --stop

dry-run:
	$(PYTHON) scripts/tron_swarm.py --dry-run $(BOT_ARGS)

test:
	go test ./...

check: test
	$(PYTHON) -m py_compile scripts/tron_swarm.py

clean:
	$(RM) $(BINARY)
