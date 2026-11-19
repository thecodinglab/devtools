# Evaluates nix/home-module.nix without home-manager, for a developer's
# command line only (no check uses it): the options the module writes are
# declared here as loose stubs. The full module is evaluated against real
# home-manager in the systems repository (P3-06); this is a quick look at
# the option types and at what the module would write. Run from the root of
# the repository, for example the misspelt backend of 8.4.1:
#
#   nix eval --impure --expr '(import ./nix/tests/module-eval.nix {
#     settings = { enable = true; multiplexer = "screen"; }; }).text'
#
# fails with the type error of the enum.
{ settings
, system ? builtins.currentSystem
, flake ? builtins.getFlake (toString ../..)
}:
let
  pkgs = flake.inputs.nixpkgs.legacyPackages.${system};
  inherit (pkgs) lib;
  loose = lib.mkOption {
    type = lib.types.attrsOf lib.types.anything;
    default = { };
  };

  stubs = {
    options = {
      assertions = lib.mkOption {
        type = lib.types.listOf lib.types.anything;
        default = [ ];
      };
      home.packages = lib.mkOption {
        type = lib.types.listOf lib.types.package;
        default = [ ];
      };
      home.homeDirectory = lib.mkOption {
        type = lib.types.str;
        default = "/home/florian";
      };
      home.profileDirectory = lib.mkOption {
        type = lib.types.str;
        default = "/etc/profiles/per-user/florian";
      };
      xdg.configFile = lib.mkOption {
        type = lib.types.attrsOf (
          lib.types.submodule {
            options.text = lib.mkOption { type = lib.types.str; };
          }
        );
        default = { };
      };
      systemd.user.services = loose;
      launchd.agents = loose;
    };
  };

  evaluated = lib.evalModules {
    modules = [
      stubs
      { _module.args.pkgs = pkgs; }
      (import ../home-module.nix flake)
      { programs.devtools = settings; }
    ];
  };
  inherit (evaluated) config;
  failed = map (a: a.message) (lib.filter (a: !a.assertion) config.assertions);
in
{
  inherit config;
  # The failed assertions, as home-manager would report them.
  inherit failed;
  text =
    if failed != [ ] then
      throw (lib.concatStringsSep "\n" failed)
    else
      config.xdg.configFile."devtools/config".text or "";
  systemd = config.systemd.user.services;
  launchd = config.launchd.agents;
}
