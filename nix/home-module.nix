# The home-manager module of devtools (8.4 of the plan). It lives in the
# devtools flake, so the generated config and the binary that parses it are
# always the same revision. flake.nix: homeModules.default = import
# ./nix/home-module.nix self;
self:
{ config
, lib
, pkgs
, ...
}:
let
  inherit (lib) mkOption types;

  cfg = config.programs.devtools;

  # The five colour tokens of 4.6 and 5a.7.1.
  colorTokens = [
    "strong"
    "muted"
    "raised"
    "selection"
    "warning"
  ];

  # The three backend names of 5.3.10, copied as text: Nix cannot read a Go
  # constant.
  backends = [
    "devtools"
    "tmux"
    "herdr"
  ];

  # session.ValidMachine (internal/session).
  validMachine = name: builtins.match "[A-Za-z0-9][A-Za-z0-9._-]{0,63}" name != null;
  validColor = value: builtins.match "#[0-9A-Fa-f]{6}" value != null;
  # A name of server.environment ends up in DEVTOOLS_PASS_ENV, a list
  # joined with commas, so it must be a plain variable name.
  validVariable = name: builtins.match "[A-Za-z_][A-Za-z0-9_]*" name != null;
  hasNewline = s: lib.hasInfix "\n" s;
  hasWhiteSpace = s: builtins.match ".*[[:space:]].*" s != null;

  bindingWords = lib.concatLists (
    lib.mapAttrsToList (chord: action: [ chord ] ++ lib.optional (action != null) action) cfg.bindings
  );

  service = import ./service.nix {
    program = "${config.home.profileDirectory}/bin/devtools";
    environment = cfg.server.environment;
    homeDirectory = config.home.homeDirectory;
    profileDirectory = config.home.profileDirectory;
  };
in
{
  options.programs.devtools = {
    enable = lib.mkEnableOption "devtools, the terminal multiplexer and project switcher";

    package = mkOption {
      type = types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "devtools.packages.\${pkgs.stdenv.hostPlatform.system}.default";
      description = "The devtools package. The default is the package of the flake this module comes from.";
    };

    roots = mkOption {
      type = types.listOf types.str;
      default = [ ];
      example = [ "~/dev" ];
      description = "Directories scanned for projects, one `root` line each. Empty: devtools uses ~/dev.";
    };

    machines = mkOption {
      type = types.attrsOf (
        types.submodule {
          options.connect = mkOption {
            type = types.bool;
            default = false;
            description = "Keep a connection to this machine for as long as the client runs.";
          };
        }
      );
      default = { };
      example = lib.literalExpression ''{ macmini.connect = true; macbook = { }; }'';
      description = "Other machines, by ssh host alias, one `machine` line each.";
    };

    bindings = mkOption {
      type = types.attrsOf (types.nullOr types.str);
      default = { };
      example = {
        "prefix+f" = "hub new";
        "prefix+q" = null;
      };
      description = "Key bindings by chord: a string is the action and its argument (`bind`), null removes the default binding (`unbind`).";
    };

    colors = mkOption {
      type = types.attrsOf (
        types.submodule {
          options = {
            light = mkOption {
              type = types.str;
              description = "The colour on a light background, #rrggbb.";
            };
            dark = mkOption {
              type = types.str;
              description = "The colour on a dark background, #rrggbb.";
            };
          };
        }
      );
      default = { };
      description = "Colour tokens (${lib.concatStringsSep ", " colorTokens}), one `color` line each.";
    };

    server = {
      enable = lib.mkEnableOption "the devtools server as a user service (systemd on Linux, launchd on macOS)";

      environment = mkOption {
        type = types.attrsOf types.str;
        default = { };
        example = {
          LANG = "en_US.UTF-8";
        };
        description = ''
          Variables of the server's environment. The server passes exactly
          these into every pane, besides its fixed allow-list, through
          DEVTOOLS_PASS_ENV, which the module adds. No store paths here: the
          unit text must not change on updates.
        '';
      };
    };

    multiplexer = mkOption {
      type = types.nullOr (types.enum backends);
      default = null;
      description = ''
        The default backend of this machine, written as the line
        `multiplexer <name>`. null writes no line and devtools' own default
        decides. DEVTOOLS_MULTIPLEXER overrides it per command; inside a
        multiplexer the line is not read. The module does not check that the
        named backend is installed, nor that server.enable is set for
        devtools: devtools answers a wrong combination at run time.
      '';
    };
  };

  config = lib.mkIf cfg.enable (
    lib.mkMerge [
      {
        assertions = [
          {
            assertion = lib.all validMachine (builtins.attrNames cfg.machines);
            message = "programs.devtools.machines: every name must be an ssh host alias of letters, digits, '.', '_' and '-', starting with a letter or a digit, at most 64 characters; got ${
              lib.concatStringsSep ", " (lib.filter (n: !validMachine n) (builtins.attrNames cfg.machines))
            }";
          }
          {
            assertion = lib.all (token: lib.elem token colorTokens) (builtins.attrNames cfg.colors);
            message = "programs.devtools.colors: every key must be one of ${lib.concatStringsSep ", " colorTokens}; got ${
              lib.concatStringsSep ", " (lib.filter (t: !lib.elem t colorTokens) (builtins.attrNames cfg.colors))
            }";
          }
          {
            assertion = lib.all (c: validColor c.light && validColor c.dark) (builtins.attrValues cfg.colors);
            message = "programs.devtools.colors: every light and dark value must be of the form #rrggbb";
          }
          {
            # One value, one line: a newline would write a second directive.
            assertion = !(lib.any hasNewline (cfg.roots ++ bindingWords));
            message = "programs.devtools: roots and bindings must not contain a newline";
          }
          {
            assertion = lib.all validVariable (builtins.attrNames cfg.server.environment);
            message = "programs.devtools.server.environment: every name must be a variable name of letters, digits and '_'";
          }
          {
            assertion = !(cfg.server.environment ? DEVTOOLS_PASS_ENV);
            message = "programs.devtools.server.environment: DEVTOOLS_PASS_ENV is set by the module from the names of server.environment";
          }
          {
            # systemd splits an unquoted Environment= value at white space,
            # and home-manager writes the entries verbatim.
            assertion =
              !pkgs.stdenv.hostPlatform.isLinux
              || !(lib.any (v: hasWhiteSpace v) (builtins.attrValues cfg.server.environment));
            message = "programs.devtools.server.environment: on Linux a value must not contain white space (systemd would split it)";
          }
        ];

        home.packages = [ cfg.package ];

        # A symlink into the store, therefore read-only; devtools writes
        # ~/.config/devtools/config.local (4.6).
        xdg.configFile."devtools/config".text = import ./config-text.nix {
          inherit (cfg)
            roots
            machines
            bindings
            colors
            multiplexer
            ;
        };
      }

      (lib.mkIf (cfg.server.enable && pkgs.stdenv.hostPlatform.isLinux) {
        systemd.user.services.devtools-server = service.systemd;
      })

      (lib.mkIf (cfg.server.enable && pkgs.stdenv.hostPlatform.isDarwin) {
        launchd.agents.devtools-server = {
          enable = true;
          config = service.launchd;
        };
      })
    ]
  );
}
