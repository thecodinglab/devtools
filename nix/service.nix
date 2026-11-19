# The service definitions of the devtools server (8.4.3 of the plan): the
# systemd user unit for Linux and the launchd agent for macOS. A pure
# function, no pkgs and no lib, so nix/tests can check both shapes on any
# system.
#
#   program           the devtools on the profile, "<profileDirectory>/bin/devtools";
#                     a symlink that follows the active generation, never a
#                     store path, so an update does not change the unit text
#                     and does not restart the server
#   environment       programs.devtools.server.environment
#   homeDirectory     home.homeDirectory (the log path of the agent)
#   profileDirectory  home.profileDirectory (the PATH of the agent)
#
# Returns { systemd = <the attrs of systemd.user.services.devtools-server>;
# launchd = <the attrs of launchd.agents.devtools-server.config>; }.
{ program
, environment
, homeDirectory
, profileDirectory
}:
let
  # The server passes exactly these variables into every pane, besides its
  # fixed allow-list (3.2.1). Joined with commas, not spaces: systemd splits
  # an unquoted Environment= value at white space and home-manager writes the
  # entries verbatim, so only the first name would survive. attrNames is
  # sorted.
  passEnv = builtins.concatStringsSep "," (builtins.attrNames environment);

  serverEnvironment = environment // {
    DEVTOOLS_PASS_ENV = passEnv;
  };
in
{
  systemd = {
    Unit = {
      Description = "devtools server: this machine's sessions";
      # Not restarted on activation (hm:modules/systemd.nix:160-172), so a
      # switch never ends the shells of the running server.
      X-SwitchMethod = "keep-old";
    };
    Install.WantedBy = [ "default.target" ];
    Service = {
      ExecStart = "${program} server";
      Restart = "always";
      RestartSec = 1;
      # SIGTERM to the server only; it ends its panes in order.
      KillMode = "mixed";
      TimeoutStopSec = 10;
      # The unit stays when the kernel kills one pane's program for memory.
      # provisional E7: the OOM runs of E7 with and without this line are
      # pending [GO]; the line is kept as 8.4.3 writes it.
      OOMPolicy = "continue";
      Environment = map (name: "${name}=${serverEnvironment.${name}}") (
        builtins.attrNames serverEnvironment
      );
    };
  };

  # The label is the one home-manager gives an agent of this name, written
  # out so it is the constant of internal/platform/types.go by construction.
  launchd = {
    Label = "org.nix-community.home.devtools-server";
    ProgramArguments = [
      program
      "server"
    ];
    RunAtLoad = true;
    KeepAlive = true;
    # Keeps macOS from throttling the process that holds every terminal.
    ProcessType = "Interactive";
    # The server runs git for the catalog, and launchd's default PATH has no
    # Nix profile. provisional E7: whether launchd sets SHELL for an agent is
    # pending [GO] (the Mac half of E7); SHELL comes from server.environment
    # (8.4.5), so nothing here depends on the answer.
    EnvironmentVariables = {
      PATH = "${profileDirectory}/bin:/run/current-system/sw/bin:/usr/bin:/bin:/usr/sbin:/sbin";
    }
    // serverEnvironment;
    StandardErrorPath = "${homeDirectory}/Library/Logs/devtools-server.log";
  };
}
