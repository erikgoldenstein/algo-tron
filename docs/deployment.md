# Deployment

Notes for running your own algo-tron server. If you only want to write a bot against the public instance, you don't need any of this.

## Build

```sh
go build -o algo-tron ./cmd/algo-tron
```

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
