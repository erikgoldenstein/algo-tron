# Deployment

Notes for running your own algo-tron server. If you only want to write a bot against the public instance, you don't need any of this.

## Build

```sh
make build
```

`make build` embeds the current Git commit in the binary. The settings modal
shows its short form; hover it to see the full value. A plain `go build` keeps
the local `dev` marker.

From the repository root, the Makefile provides the common local commands:

```sh
make build
make run
make dev
make run-bot BOT_ARGS='--count 64 --prefix workshop --lobby workshop'
make stop-bots BOT_ARGS='--pid-file scripts/.tron-swarm-workshop.pid'
```

`make dev` watches `cmd/`, `go.mod`, and `go.sum`, restarting `go run` after a
change. The viewer reconnects and reloads its page after the restart. The
swarm is dependency-free Python; use a unique `--prefix` and `--pid-file` for
each additional lobby.

## Run locally

```sh
./algo-tron \
  -tcp 127.0.0.1:4000 \
  -public-tcp tron.erik.gdn:4000 \
  -view 127.0.0.1:3000 \
  -public-view tron.erik.gdn \
  -public-view-scheme https
```

Options:

- `-tcp`: local raw TCP game listener.
- `-view`: local HTTP viewer listener.
- `-public-tcp`: public TCP endpoint shown in the viewer UI.
- `-public-view`: public viewer endpoint shown in the viewer UI.
- `-public-view-scheme`: `http` or `https`, only affects what the viewer UI displays.
- `-data-dir`: directory holding the SQLite player database and HMAC secret. Defaults to a temp directory; set this for persistence.
- `-geo-dir`: directory holding the GeoLite2 `.mmdb` files (default `geo`). Read-only enrichment, kept separate from `-data-dir`. See [persistence.md](persistence.md#geolite-setup).
- `-setup-geo`: download the GeoLite2 databases into `-geo-dir` and exit (one-off setup; normal startup never downloads). See [persistence.md](persistence.md#geolite-setup).
- `-schedule-url`: URL for an optional talk schedule JSON shown in the viewer (only used at chaos events). Omit to hide the schedule panel.
- `-proxy-protocol`: expect HAProxy PROXY protocol v1 headers on incoming TCP connections (use behind a TCP proxy that preserves client IPs).
- `-metrics`: separate Prometheus `/metrics` listener address (e.g. `127.0.0.1:9090`). Empty disables it. Unauthenticated — bind to localhost.
- `-view-metrics-auth`: if set (`user:pass`), also expose `/metrics` on the viewer HTTP server protected by HTTP Basic auth (Prometheus-compatible). Useful when you'd rather scrape over the same TLS-terminated host as the viewer.

The viewer is available at `/` and `/screen`. `/screen` selects the global
scoreboard and board-local chat. Add `?lobby=name` to keep automatic board
selection in that lobby while it exists. Lobby creation, editing, and removal
are available to an authenticated administrator in the settings UI.

The intended deployment model is to run the Go service on localhost behind nginx on a single hostname:

- `tron.erik.gdn:443` routes to the HTTP viewer server.
- `tron.erik.gdn:4000` routes to the raw TCP game server.

## NixOS flake

This repo exposes a package and a NixOS module:

```nix
{
  inputs.algo-tron.url = "github:erikgoldenstein/algo-tron";

  outputs = { self, nixpkgs, algo-tron, ... }: {
    nixosConfigurations.server = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        algo-tron.nixosModules.default
        {
          services.algo-tron = {
            enable = true;
            tcp.listen = "127.0.0.1:4000";
            view.listen = "127.0.0.1:3000";
            tcp.publicAddress = "tron.erik.gdn:4000";
            view.publicAddress = "tron.erik.gdn";
            view.publicScheme = "https";
            # Optional:
            # tcp.proxyProtocol = true;
            # dataDir = "/var/lib/algo-tron";
            # scheduleURL = "https://example.org/schedule.json"; # used for chaos events
            # metrics.listen = "127.0.0.1:9090";                 # separate unauthenticated /metrics listener
            # view.metricsAuth = "prometheus:s3cret";            # OR expose /metrics on the viewer port with Basic auth
            #                                                    # (consider lib.fileContents + sops-nix / agenix in production)
            # openFirewall = true;                               # open the tcp.listen / view.listen ports
          };
        }
      ];
    };
  };
}
```

## Nginx

The viewer is normal HTTP with websockets, so proxy it from an HTTP `server` block:

```nginx
server {
  listen 443 ssl;
  server_name tron.erik.gdn;

  location / {
    proxy_pass http://127.0.0.1:3000;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
  }
}
```

The game endpoint is raw TCP, so route it with nginx `stream {}`:

```nginx
stream {
  upstream algo_tron_tcp {
    server 127.0.0.1:4000;
  }

  server {
    listen 4000;
    proxy_pass algo_tron_tcp;
  }
}
```

Both endpoints live on the same hostname (`tron.erik.gdn`): the viewer on `443` (HTTPS, terminated by nginx) and the raw TCP game server on `4000`. Make sure nothing else on the box is bound to `4000`, and open it in any upstream firewall.

## Deploy script

`deploy.sh` deploys to the `tron-prod-vm` SSH alias by default and can also
provision the current machine with `--local`.
Routine production updates should use the `prod` GitHub Actions workflow: it
runs the full test and benchmark gate, then installs the resulting binary. The
script is useful when preparing a new VM or when CI is unavailable.

It requires root and supports Debian/Ubuntu and Rocky/RHEL:

```sh
sudo ./deploy.sh
```

The script defaults to `tron.erik.gdn`. On an already-provisioned host it
automatically reuses the root-only Cloudflare credentials at
`/root/.secrets/cloudflare.ini`; pass `--domain` or `--cloudflare-token` only
when targeting a different domain or replacing the token.
TCP port `4000` and HTTPS port `443` are also used by default, without prompts.
Use `--interactive` when you want the old prompt-driven setup, or pass the
individual flags directly.

From a local checkout, `sudo ./deploy.sh` forwards the script to
`tron-prod-vm` over SSH and runs it there. Use `--host alias` for another SSH
alias, or `--local` to run directly on the current machine.

For a routine manual binary update on an already-provisioned host, use
deploy-only mode. It leaves nginx, certificates, firewall rules, and host
hardening alone:

```sh
sudo ./deploy.sh --deploy-only
```

Use `--acme-email address@example.com` during provisioning if the certificate
authority should have a renewal contact; otherwise the script uses Certbot's
no-email mode.

Inspect the planned configuration first with:

```sh
sudo ./deploy.sh --dry-run --domain tron.example.com
```

When run from a checkout, only committed `HEAD` is built. A dirty checkout is
rejected; `--allow-dirty` permits the command but still deploys committed
`HEAD`. When run through `curl`, use `--ref` to select a branch. The deployed
commit is recorded in `/opt/algo-tron/release`.

Every redeploy makes a consistent SQLite backup of the player database,
secret, and admin password under `/var/backups/algo-tron`, retaining seven
backups by default. Change this with `--backup-dir` and `--backup-keep`, or
skip it explicitly with `--no-backup`. The previous five binaries are retained
under `/opt/algo-tron/releases`; restore the newest previous one with:

```sh
sudo ./deploy.sh --rollback
```

Rollback creates a fresh state backup, restarts the service, waits for
`/healthz`, and runs the viewer smoke test. The standalone check can be run
against any edge URL:

```sh
deploy/smoke.sh https://tron.example.com
```

Certificate renewal is independent of application deployment:

```sh
sudo deploy/renew-cert.sh
```

The package-provided Certbot timer can renew automatically; the helper is also
convenient for an explicit renewal check.

The default deployment also enables a host firewall, auditd, an SSH fail2ban
jail, and automatic security updates. Disable individual measures with
`--no-firewall`, `--no-auditd`, `--no-fail2ban`, or `--no-upgrades`; disable all
with `--no-hardening`. `--no-firewall` is appropriate when an upstream or
cloud firewall is authoritative.

The service runs as the unprivileged `tron` user with systemd filesystem and
privilege restrictions. nginx accepts only the configured hostname, applies
basic security headers and request limits, and keeps the application listeners
bound to localhost.
