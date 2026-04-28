{
  description = "Edna note server";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
    }:
    let
      module =
        {
          config,
          lib,
          pkgs,
          ...
        }:
        let
          cfg = config.services.edna;
        in
        {
          options.services.edna = {
            enable = lib.mkEnableOption "Edna note server";

            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.system}.default;
              description = "Edna package to run.";
            };

            listen = lib.mkOption {
              type = lib.types.str;
              default = "127.0.0.1:9325";
              description = "HTTP listen address.";
            };

            dataDir = lib.mkOption {
              type = lib.types.path;
              default = "/var/lib/edna";
              description = "Persistent Edna data directory.";
            };

            user = lib.mkOption {
              type = lib.types.str;
              default = "edna";
              description = "User account for the service.";
            };

            group = lib.mkOption {
              type = lib.types.str;
              default = "edna";
              description = "Group account for the service.";
            };
          };

          config = lib.mkIf cfg.enable {
            users.groups.${cfg.group} = { };
            users.users.${cfg.user} = {
              isSystemUser = true;
              group = cfg.group;
              home = cfg.dataDir;
              createHome = true;
            };

            systemd.tmpfiles.rules = [
              "d ${cfg.dataDir} 0750 ${cfg.user} ${cfg.group} - -"
            ];

            systemd.services.edna = {
              description = "Edna note server";
              wantedBy = [ "multi-user.target" ];
              after = [ "network-online.target" ];
              wants = [ "network-online.target" ];
              serviceConfig = {
                Type = "simple";
                User = cfg.user;
                Group = cfg.group;
                WorkingDirectory = cfg.dataDir;
                ExecStart = "${lib.getExe cfg.package} -run-prod -listen ${cfg.listen} -data-dir ${cfg.dataDir}";
                Restart = "on-failure";
                RestartSec = 5;
                ReadWritePaths = [ cfg.dataDir ];
                NoNewPrivileges = true;
                PrivateTmp = true;
                ProtectSystem = "strict";
                ProtectHome = true;
              };
            };
          };
        };
    in
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs { inherit system; };

        bunDeps = pkgs.stdenvNoCC.mkDerivation {
          pname = "edna-bun-deps";
          version = "1.39";
          src = ./.;

          nativeBuildInputs = [ pkgs.bun ];

          dontConfigure = true;
          dontFixup = true;

          buildPhase = ''
            runHook preBuild
            export HOME="$TMPDIR"
            bun install --frozen-lockfile --no-progress
            runHook postBuild
          '';

          installPhase = ''
            runHook preInstall
            mkdir -p "$out"
            cp -R node_modules "$out/node_modules"
            runHook postInstall
          '';

          outputHashAlgo = "sha256";
          outputHashMode = "recursive";
          outputHash = pkgs.lib.fakeHash;
        };

        edna = pkgs.buildGoModule {
          pname = "edna";
          version = "1.39";
          src = ./.;

          vendorHash = pkgs.lib.fakeHash;

          nativeBuildInputs = [ pkgs.bun ];

          preBuild = ''
            cp -R ${bunDeps}/node_modules ./node_modules
            chmod -R u+w ./node_modules
            bun run build
          '';

          ldflags = [
            "-s"
            "-w"
          ];

          doCheck = false;
        };
      in
      {
        packages.default = edna;
        packages.edna = edna;

        apps.default = {
          type = "app";
          program = "${pkgs.lib.getExe edna}";
        };

        devShells.default = pkgs.mkShell {
          packages = [
            pkgs.bun
            pkgs.go
            pkgs.nixfmt-rfc-style
          ];
        };

        formatter = pkgs.nixfmt-rfc-style;
      }
    )
    // {
      nixosModules.default = module;
      nixosModules.edna = module;
    };
}
