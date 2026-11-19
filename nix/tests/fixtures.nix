# Fixtures of the checks of 8.4.4. Option values as the module hands them to
# nix/config-text.nix and nix/service.nix.
let
  # The five tokens with the desktop's values (8.4.2).
  colors = {
    strong = {
      light = "#111214";
      dark = "#e6e7eb";
    };
    muted = {
      light = "#55585f";
      dark = "#a6a8b0";
    };
    raised = {
      light = "#e3e4e8";
      dark = "#30323a";
    };
    warning = {
      light = "#b04a00";
      dark = "#ec8a3f";
    };
    selection = {
      light = "#d6dde9";
      dark = "#3a4150";
    };
  };

  machines = {
    macmini.connect = true;
    macbook = { };
  };
in
{
  # Every directive once, and `multiplexer devtools`: the backend the check
  # would use anyway, so the line is written and parsed without depending on
  # how an unavailable backend is reported. The roots are not sorted, to show
  # a list keeps its order. Expected text: expected-config.
  config = {
    roots = [
      "~/work"
      "~/dev"
    ];
    inherit machines colors;
    bindings = {
      "prefix+f" = "hub new";
      "prefix+q" = null;
    };
    multiplexer = "devtools";
  };

  # The desktop as 8.4.5 sets it: no multiplexer (F38), no roots, no
  # bindings. Expected text: expected-config-default, the whole file of 8.4.2.
  config-default = {
    roots = [ ];
    inherit machines colors;
    bindings = { };
    multiplexer = null;
  };

  # service.nix with profile paths, as the module passes them: on the desktop
  # and on a Mac.
  services = {
    linux = {
      program = "/etc/profiles/per-user/florian/bin/devtools";
      homeDirectory = "/home/florian";
      profileDirectory = "/etc/profiles/per-user/florian";
      environment = {
        SSH_AUTH_SOCK = "/run/user/1000/ssh-agent";
        SHELL = "/etc/profiles/per-user/florian/bin/zsh";
        LANG = "en_US.UTF-8";
      };
    };
    darwin = {
      program = "/etc/profiles/per-user/florian/bin/devtools";
      homeDirectory = "/Users/florian";
      profileDirectory = "/etc/profiles/per-user/florian";
      environment = {
        SHELL = "/etc/profiles/per-user/florian/bin/zsh";
        LANG = "en_US.UTF-8";
      };
    };
    bare = {
      program = "/home/florian/.nix-profile/bin/devtools";
      homeDirectory = "/home/florian";
      profileDirectory = "/home/florian/.nix-profile";
      environment = { };
    };
  };
}
