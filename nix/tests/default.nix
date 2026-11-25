# The checks of the home module (8.4.4 of the plan), run by `nix flake
# check`. No home-manager input is needed: config-text.nix and service.nix
# are pure functions, and the full module is evaluated in the systems
# repository (P3-06).
#
#   config-text     the generated text of both fixtures equals the expected
#                   file, and the real binary parses it
#   service-stable  no output of service.nix holds a store path
#   service-shape   the unit and the agent have the properties of 8.4.3
{ pkgs, devtools }:
let
  inherit (pkgs) lib;

  fixtures = import ./fixtures.nix;
  configText = import ../config-text.nix;
  service = import ../service.nix;

  # A check whose verdict is computed at evaluation time: it builds when
  # failures is empty and otherwise prints every failure and fails.
  verdict =
    name: failures: report:
    pkgs.runCommand name
      {
        failures = lib.concatStringsSep "\n" failures;
        inherit report;
      }
      ''
        if [ -n "$failures" ]; then
          printf '%s\n' "$failures" >&2
          exit 1
        fi
        printf '%s\n' "$report" > $out
      '';

  # Each failing comparison is one line of the verdict.
  expect =
    what: got: want:
    lib.optional (got != want) "${what}: got ${builtins.toJSON got}, want ${builtins.toJSON want}";

  services = lib.mapAttrs (_: service) fixtures.services;
in
{
  config-text =
    pkgs.runCommand "devtools-check-config-text"
      {
        nativeBuildInputs = [
          devtools
          pkgs.diffutils
        ];
        generated = pkgs.writeText "config" (configText fixtures.config);
        expected = ./expected-config;
        generatedDefault = pkgs.writeText "config-default" (configText fixtures.config-default);
        expectedDefault = ./expected-config-default;
      }
      ''
        # The guard against the config trap of 4.6: the binary of this
        # revision loads the text this revision generates. `devtools list`
        # reads the config and scans the roots; it selects no backend, and the
        # sandbox has no tmux and no herdr anyway.
        unset DEVTOOLS_CONFIG DEVTOOLS_ROOT DEVTOOLS_MULTIPLEXER
        parse() {
          export HOME="$TMPDIR/$2/home" XDG_CONFIG_HOME="$TMPDIR/$2/config"
          mkdir -p "$HOME/dev" "$HOME/work" "$XDG_CONFIG_HOME/devtools"
          cp "$1" "$XDG_CONFIG_HOME/devtools/config"
          if ! devtools list > "$TMPDIR/$2/list" 2>&1; then
            echo "devtools list failed on the $2 fixture:" >&2
            cat "$TMPDIR/$2/list" >&2
            exit 1
          fi
        }
        diff -u "$expected" "$generated"
        diff -u "$expectedDefault" "$generatedDefault"
        parse "$generated" config
        parse "$generatedDefault" config-default
        cp "$generated" $out
      '';

  service-stable =
    let
      json = lib.mapAttrs (_: builtins.toJSON) services;
      leaks = lib.filterAttrs
        (
          _: text: lib.hasInfix "/nix/store" text || lib.hasInfix builtins.storeDir text
        )
        json;
    in
    verdict "devtools-check-service-stable"
      (lib.mapAttrsToList
        (
          name: text: "service.nix of the ${name} fixture holds a store path: ${text}"
        )
        leaks)
      (lib.concatStringsSep "\n" (lib.attrValues json));

  service-shape =
    let
      shape =
        name: s:
        let
          f = fixtures.services.${name};
          names = builtins.attrNames f.environment;
          env = s.systemd.Service.Environment;
          passEnvLine = lib.filter (lib.hasPrefix "DEVTOOLS_PASS_ENV=") env;
          prefix = "${name}: ";
        in
        map (line: prefix + line) (
          expect "launchd ProcessType" s.launchd.ProcessType "Interactive"
          ++ expect "launchd KeepAlive" s.launchd.KeepAlive true
          ++ expect "launchd RunAtLoad" s.launchd.RunAtLoad true
          ++ expect "launchd Label" s.launchd.Label "org.nix-community.home.devtools-server"
          ++ expect "launchd ExitTimeOut" s.launchd.ExitTimeOut s.systemd.Service.TimeoutStopSec
          ++ expect "systemd StartLimitIntervalSec" s.systemd.Unit.StartLimitIntervalSec 0
          ++ expect "systemd RestartMaxDelaySec" s.systemd.Service.RestartMaxDelaySec 10
          ++ expect "launchd ProgramArguments" s.launchd.ProgramArguments [
            f.program
            "server"
          ]
          ++ expect "launchd PATH starts with the profile" (lib.hasPrefix "${f.profileDirectory}/bin:" s.launchd.EnvironmentVariables.PATH) true
          ++ expect "systemd X-SwitchMethod" s.systemd.Unit.X-SwitchMethod "keep-old"
          ++ expect "systemd Restart" s.systemd.Service.Restart "always"
          ++ expect "systemd OOMPolicy" s.systemd.Service.OOMPolicy "continue"
          ++ expect "systemd KillMode" s.systemd.Service.KillMode "mixed"
          ++ expect "systemd ExecStart" s.systemd.Service.ExecStart "${f.program} server"
          ++ expect "systemd WantedBy" s.systemd.Install.WantedBy [ "default.target" ]
          # DEVTOOLS_PASS_ENV: exactly the names of the fixture, joined with
          # commas, in both definitions.
          ++ expect "systemd DEVTOOLS_PASS_ENV entries" passEnvLine [
            "DEVTOOLS_PASS_ENV=${lib.concatStringsSep "," names}"
          ]
          ++ expect "launchd DEVTOOLS_PASS_ENV"
            (lib.sort lib.lessThan (
              lib.filter (n: n != "") (lib.splitString "," s.launchd.EnvironmentVariables.DEVTOOLS_PASS_ENV)
            ))
            names
          # Every variable of the fixture reaches both definitions unchanged.
          ++ expect "systemd Environment" (lib.sort lib.lessThan env) (
            lib.sort lib.lessThan (passEnvLine ++ lib.mapAttrsToList (n: v: "${n}=${v}") f.environment)
          )
          ++ expect "launchd EnvironmentVariables"
            (removeAttrs s.launchd.EnvironmentVariables [
              "PATH"
              "DEVTOOLS_PASS_ENV"
            ])
            f.environment
          # systemd splits an unquoted Environment= value at white space.
          ++ expect "systemd Environment entries with white space"
            (lib.filter
              (
                e: builtins.match ".*[[:space:]].*" e != null
              )
              env) [ ]
        );

      # The names, typed out: the joined form is the contract with the
      # server (3.2.1), not whatever service.nix computes.
      literal =
        expect "linux DEVTOOLS_PASS_ENV" services.linux.launchd.EnvironmentVariables.DEVTOOLS_PASS_ENV "LANG,SHELL,SSH_AUTH_SOCK"
        ++ expect "darwin DEVTOOLS_PASS_ENV" services.darwin.launchd.EnvironmentVariables.DEVTOOLS_PASS_ENV "LANG,SHELL"
        ++ expect "bare DEVTOOLS_PASS_ENV" services.bare.launchd.EnvironmentVariables.DEVTOOLS_PASS_ENV "";
    in
    verdict "devtools-check-service-shape"
      (
        lib.concatLists (lib.mapAttrsToList shape services) ++ literal
      ) "devtools service definitions: ${lib.concatStringsSep ", " (builtins.attrNames services)}";
}
