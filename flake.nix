{
  description = "Minimal Go implementation of GPN Tron";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        package = pkgs.buildGoModule {
          pname = "algo-tron";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-QNwJNR0SKoRorUPYE1AiagufK0u+eYnFuKLrYv6PomY=";
          subPackages = [ "cmd/algo-tron" ];
          ldflags = [ "-s" "-w" ];
        };
      in
      {
        packages.default = package;
        packages.algo-tron = package;

        apps.default = {
          type = "app";
          program = "${package}/bin/algo-tron";
        };

        devShells.default = pkgs.mkShell {
          packages = [ pkgs.go ];
        };
      }) // {
      nixosModules.default = { config, lib, pkgs, ... }:
        let
          cfg = config.services.algo-tron;
          package = cfg.package;
          parsePort = listen:
            let
              matches = builtins.match ".*:([0-9]+)$" listen;
            in
            if matches == null then null else lib.toInt (builtins.elemAt matches 0);
          args = [
            "-tcp" cfg.tcp.listen
            "-view" cfg.view.listen
          ] ++ lib.optional cfg.tcp.proxyProtocol "-proxy-protocol" ++ [
            "-public-tcp" cfg.tcp.publicAddress
            "-public-view" cfg.view.publicAddress
            "-public-view-scheme" cfg.view.publicScheme
            "-data-dir" cfg.dataDir
          ] ++ lib.optionals (cfg.scheduleURL != "") [
            "-schedule-url" cfg.scheduleURL
          ] ++ lib.optionals (cfg.metrics.listen != "") [
            "-metrics" cfg.metrics.listen
          ] ++ lib.optionals (cfg.view.metricsAuth != "") [
            "-view-metrics-auth" cfg.view.metricsAuth
          ];
        in
        {
          options.services.algo-tron = {
            enable = lib.mkEnableOption "Go Tron game and viewer server";

            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
              defaultText = lib.literalExpression "self.packages.\${pkgs.stdenv.hostPlatform.system}.default";
              description = "algo-tron package to run.";
            };

            user = lib.mkOption {
              type = lib.types.str;
              default = "algo-tron";
              description = "User account for the service.";
            };

            group = lib.mkOption {
              type = lib.types.str;
              default = "algo-tron";
              description = "Group account for the service.";
            };

            dataDir = lib.mkOption {
              type = lib.types.str;
              default = "/var/lib/algo-tron";
              description = "Directory holding the SQLite player database and HMAC secret.";
            };

            scheduleURL = lib.mkOption {
              type = lib.types.str;
              default = "";
              example = "https://example.org/schedule.json";
              description = "URL for the optional talk schedule JSON shown in the viewer UI. Leave empty to hide the schedule panel.";
            };

            tcp.listen = lib.mkOption {
              type = lib.types.str;
              default = "127.0.0.1:4000";
              example = ":4000";
              description = "Local TCP game protocol listen address.";
            };

            tcp.publicAddress = lib.mkOption {
              type = lib.types.str;
              default = "play-tron.erik.gdn:443";
              description = "Public TCP game endpoint shown in the viewer UI.";
            };

            tcp.proxyProtocol = lib.mkOption {
              type = lib.types.bool;
              default = false;
              description = "Accept HAProxy PROXY protocol v1 headers on TCP game connections.";
            };

            view.listen = lib.mkOption {
              type = lib.types.str;
              default = "127.0.0.1:3000";
              example = ":3000";
              description = "Local HTTP viewer listen address.";
            };

            view.publicAddress = lib.mkOption {
              type = lib.types.str;
              default = "view-tron.erik.gdn:443";
              description = "Public viewer endpoint shown in the viewer UI.";
            };

            view.publicScheme = lib.mkOption {
              type = lib.types.enum [ "http" "https" ];
              default = "https";
              description = "Public viewer scheme shown in the viewer UI.";
            };

            view.metricsAuth = lib.mkOption {
              type = lib.types.str;
              default = "";
              example = "prometheus:s3cret";
              description = ''
                If non-empty (`user:pass`), also expose Prometheus `/metrics` on the viewer HTTP listener,
                protected by HTTP Basic auth (Prometheus-compatible).
                The literal value ends up in the world-readable Nix store and in `systemctl show` —
                use `lib.fileContents` with sops-nix / agenix if that matters for your threat model.
              '';
            };

            metrics.listen = lib.mkOption {
              type = lib.types.str;
              default = "";
              example = "127.0.0.1:9090";
              description = "Prometheus /metrics listen address. Leave empty to disable. Bind to localhost — the endpoint is unauthenticated.";
            };

            openFirewall = lib.mkOption {
              type = lib.types.bool;
              default = false;
              description = "Open firewall ports parsed from tcp.listen and view.listen when they bind publicly.";
            };
          };

          config = lib.mkIf cfg.enable {
            users.groups.${cfg.group} = { };
            users.users.${cfg.user} = {
              isSystemUser = true;
              group = cfg.group;
              home = "/var/lib/algo-tron";
              createHome = true;
            };

            systemd.services.algo-tron = {
              description = "Go Tron game and viewer server";
              wantedBy = [ "multi-user.target" ];
              after = [ "network-online.target" ];
              wants = [ "network-online.target" ];
              serviceConfig = {
                Type = "simple";
                User = cfg.user;
                Group = cfg.group;
                StateDirectory = "algo-tron";
                ExecStart = "${package}/bin/algo-tron ${lib.escapeShellArgs args}";
                Restart = "on-failure";
                RestartSec = "5s";
                NoNewPrivileges = true;
                PrivateTmp = true;
                ProtectHome = true;
                ProtectSystem = "strict";
                ReadWritePaths = [ cfg.dataDir ];
              };
            };

            networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall (
              lib.filter (port: port != null) [
                (parsePort cfg.tcp.listen)
                (parsePort cfg.view.listen)
              ]
            );
          };
        };
    };
}
