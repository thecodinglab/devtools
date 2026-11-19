# The text of ~/.config/devtools/config (8.4.2 of the plan) from the values
# of the options of programs.devtools (8.4.1). A pure function: no pkgs, no
# lib, so the checks of nix/tests can call it without home-manager.
#
# Lines are written in this order, attribute sets sorted by name (the order
# of builtins.attrNames): `multiplexer` (when set), `root` (in list order),
# `machine`, `bind` and `unbind`, `color`. Every line ends with a newline;
# no option set gives the empty text. The grammar is the one of
# internal/config (4.6); the values are checked by the module's assertions
# (nix/home-module.nix), not here.
{ roots ? [ ]
, machines ? { }
, bindings ? { }
, colors ? { }
, multiplexer ? null
}:
let
  sortedLines = set: f: map (name: f name set.${name}) (builtins.attrNames set);

  multiplexerLines = if multiplexer == null then [ ] else [ "multiplexer ${multiplexer}" ];

  rootLines = map (root: "root ${root}") roots;

  machineLines = sortedLines machines (
    name: machine: "machine ${name}" + (if machine.connect or false then " connect" else "")
  );

  # A binding of null removes a default binding; a string is the action and
  # its argument, as the `bind` line takes them.
  bindingLines = sortedLines bindings (
    chord: action: if action == null then "unbind ${chord}" else "bind ${chord} ${action}"
  );

  colorLines = sortedLines colors (token: value: "color ${token} ${value.light} ${value.dark}");

  lines = multiplexerLines ++ rootLines ++ machineLines ++ bindingLines ++ colorLines;
in
builtins.concatStringsSep "" (map (line: line + "\n") lines)
