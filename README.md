
A minimal Go reimplementation of [freehuntx/gpn-tron](https://github.com/freehuntx/gpn-tron)
It keeps the original TCP bot protocol and serves a public viewer UI over HTTP. The intended deployment model is to run the Go service on localhost and put nginx in front of it:

- `tron.erik.gdn:4000` routes to the raw TCP game server.
- `tron.erik.gdn:443` routes to the HTTP viewer server.

## Build

```sh
go build -o go-tron ./cmd/gpn-tron-go
```

## Run Locally

```sh
./go-tron \
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
- `-data-dir`: directory holding the SQLite player database, HMAC secret, and rotated log files. Defaults to a temp directory; set this for persistence.
- `-schedule-url`: URL for an optional talk schedule JSON shown in the viewer (only used at chaos events). Omit to hide the schedule panel.
- `-proxy-protocol`: expect HAProxy PROXY protocol v1 headers on incoming TCP connections (use behind a TCP proxy that preserves client IPs).

## NixOS Flake Deployment

This repo exposes a package and a NixOS module:

```nix
{
  inputs.go-tron.url = "github:erikgoldenstein/go-tron";

  outputs = { self, nixpkgs, go-tron, ... }: {
    nixosConfigurations.server = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        go-tron.nixosModules.default
        {
          services.go-tron = {
            enable = true;
            tcp.listen = "127.0.0.1:4000";
            view.listen = "127.0.0.1:3000";
            tcp.publicAddress = "tron.erik.gdn:4000";
            view.publicAddress = "tron.erik.gdn";
            view.publicScheme = "https";
            # Optional:
            # tcp.proxyProtocol = true;
            # dataDir = "/var/lib/go-tron";
            # scheduleURL = "https://example.org/schedule.json"; # used for chaos events
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
  upstream go_tron_tcp {
    server 127.0.0.1:4000;
  }

  server {
    listen 4000;
    proxy_pass go_tron_tcp;
  }
}
```

If the same nginx instance terminates HTTPS for the viewer on `443`, the raw TCP game endpoint needs a different IP, a different port, or a TLS/SNI-aware stream setup that can separate `play-tron.erik.gdn` from `view-tron.erik.gdn`.
