{
  description = "devtools CLI";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      supportedSystems = [
        "aarch64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];

      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;

      # What the package and checks.go-test share: the source, the version
      # and the one hash of the vendor tree (8.5 item 1). The hash is written
      # by the orchestrator only (O-00, O-01). cgo is on and is not set here:
      # buildGoModule takes the setting from the compiler, which enables cgo
      # on every native build (8.5 item 2).
      goModule = {
        version = "0.1.0";
        src = self;
        vendorHash = "sha256-01DS/OhAUg+7F8wVXk/GcwTaIJ85g4/vl7UZzTH8xeo=";
      };
    in
    {
      formatter = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        pkgs.nixpkgs-fmt
      );

      packages = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        rec {
          default = pkgs.buildGo126Module (
            goModule
            // {
              pname = "devtools";

              subPackages = [ "cmd/devtools" ];

              nativeBuildInputs = [ pkgs.pkg-config ];
              buildInputs = [ libghostty-vt ];

              nativeCheckInputs = [ pkgs.git ];

              meta = {
                description = "devtools CLI";
                homepage = "https://github.com/thecodinglab/devtools";
                license = pkgs.lib.licenses.mit;
                mainProgram = "devtools";
              };
            }
          );

          libghostty-vt = pkgs.callPackage ./nix/libghostty-vt.nix { };
        }
      );

      # The home-manager module (8.4, P3-03): the generated config and the
      # binary that parses it are always the same revision.
      homeModules.default = import ./nix/home-module.nix self;

      apps = forAllSystems (
        system:
        let
          pkg = self.packages.${system}.default;
        in
        {
          default = {
            type = "app";
            program = "${pkg}/bin/devtools";
            meta.description = "Run the devtools CLI";
          };
        }
      );

      checks = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
          libghostty-vt = self.packages.${system}.libghostty-vt;
        in
        {
          default = self.packages.${system}.default;

          # The checks of the home module (8.4.4, P3-03).
          inherit
            (import ./nix/tests {
              inherit pkgs;
              devtools = self.packages.${system}.default;
            })
            config-text
            service-stable
            service-shape
            ;

          # go test ./... inside the build sandbox, against the real library.
          # E8 (P3-01): a PTY opens in checkPhase on x86_64-linux, so the PTY
          # tests and internal/tmux/real_test.go run here and
          # DEVTOOLS_TEST_NO_PTY is not set. provisional E8: the darwin half
          # of E8 has not run; this assumes a PTY opens in the darwin sandbox
          # too. If it does not, set DEVTOOLS_TEST_NO_PTY = "1" for darwin only
          # (lib.optionalAttrs stdenv.hostPlatform.isDarwin) and run the PTY
          # tests in the dev shell on the Mac at every checkpoint (8.5, RK9).
          go-test = pkgs.buildGo126Module (
            goModule
            // {
              pname = "devtools-tests";

              doCheck = true;
              nativeCheckInputs = [
                pkgs.git
                pkgs.tmux
              ];
              nativeBuildInputs = [ pkgs.pkg-config ];
              buildInputs = [ libghostty-vt ];

              # internal/e2e runs the built binary (level 6 of 10.1) and never
              # runs in the sandbox: it is left out by path here, not skipped
              # (8.5, 10.1, P3-05a), and runs in the dev shell at every
              # checkpoint as part of go test ./... (6.7).
              excludedPackages = [ "internal/e2e" ];

              # The sandbox's HOME (/homeless-shelter) does not exist and has
              # no git configuration. The tests of internal/cli start a fake
              # herdr server in HOME and make commits in scratch repositories
              # whose branch they expect to be main, as on a developer's
              # machine; give them an empty HOME with a git identity.
              preCheck = ''
                export HOME="$(mktemp -d)"
                git config --global user.name "devtools tests"
                git config --global user.email "tests@devtools.invalid"
                git config --global init.defaultBranch main

                # Guard of the package list the check phase runs: internal/e2e
                # is absent, and the packages 8.5 runs in the sandbox (the
                # engine binding and the real tmux test) are still there.
                # getGoDirs is the helper buildGoModule's build phase defines
                # and its check phase iterates over.
                type getGoDirs >/dev/null 2>&1 || {
                  echo "go-test: buildGoModule no longer defines getGoDirs" >&2
                  exit 1
                }
                goTestDirs="$(getGoDirs test)"
                if grep -qx './internal/e2e' <<<"$goTestDirs"; then
                  echo "go-test: internal/e2e must be left out of the sandbox (8.5)" >&2
                  exit 1
                fi
                for required in ./internal/vt ./internal/tmux; do
                  grep -qx "$required" <<<"$goTestDirs" || {
                    echo "go-test: $required is missing from the sandbox tests" >&2
                    exit 1
                  }
                done
              '';

              installPhase = ''
                mkdir -p $out
                touch $out/passed
              '';
            }
          );
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
          libghostty-vt = self.packages.${system}.libghostty-vt;
        in
        {
          default = pkgs.mkShell {
            packages = [
              pkgs.go_1_26
              pkgs.gopls
              pkgs.gotools
              pkgs.git
              pkgs.tmux
              pkgs.pkg-config
            ];
            buildInputs = [ libghostty-vt ];
          };
        }
      );
    };
}
