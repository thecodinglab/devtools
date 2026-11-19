#!/bin/sh
<!-- : 2<<'---'
The two lines above and the last line let `shellcheck scripts/verify/*`
read this file as one empty shell command.
-->

# scripts/verify: rehearsal and cutover

One command for each step of the rehearsal (PLAN 9.2), the preflight (9.3)
and the post-switch checks (9.5). POSIX shell, the same on all three
machines. Package P3-04 of the plan.

| Script | What it does | Changes |
| --- | --- | --- |
| `rc-install <flake-ref>` | `nix build <ref> -o ~/.cache/devtools-rc/result`, prints the revision and keeps it in `~/.cache/devtools-rc/revision` | the result link |
| `rc-server start` / `stop` / `status` | the rehearsal server: on Linux the transient user unit `devtools-rc` (`systemd-run`), on macOS `/tmp/devtools-rc.plist` bootstrapped into `gui/<uid>` as `org.nix-community.home.devtools-rc`. Refuses to start when the real `devtools-server` exists | starts and stops a service: [GO] |
| `rc-ssh` | stand-in for `ssh` on the PATH of the rehearsal client: the trailing `devtools bridge` becomes `.cache/devtools-rc/result/bin/devtools bridge` | nothing |
| `rc-attach [--practice]` | the rehearsal client, in a plain terminal only | nothing |
| `rc-measure <alias> <label>` | the three real-machine tests of P3-05b against the rehearsal servers (10.4) | one scratch session on each machine; ssh: [GO] |
| `preflight` | PF-1 to PF-11; exit 0 only when all pass | nothing |
| `preflight --record` | stores the rollback target of PF-9 as a GC root | [GO] |
| `postcheck` | PC-1 to PC-13 (PC-6 retired) | a tmux server on its own socket `devtools-postcheck`, killed again |
| `lib.sh` | what the scripts share; sourced, never run | |

Every script takes `--print` where it would start something, and prints
the command instead: `rc-install --print`, `rc-server --print` (and
`--print-env`, the variables every rehearsal pane gets), `rc-ssh --print`,
`rc-attach --print`, `rc-measure --print`.

## Before the rehearsal

- The candidate: `scripts/verify/rc-install /home/florian/dev/devtools/replace-herdr`
  on the desktop. On a Mac the same with the reference of 9.2 step 1.
- The tests for `rc-measure`: a checkout of the same revision in
  `~/.cache/devtools-rc/src` (on a Mac: `git clone -b replace-herdr desktop:/home/florian/dev/devtools/.bare ~/.cache/devtools-rc/src`, [GO]).
- The client config: `~/.cache/devtools-rc/config/devtools/config`, typed
  by hand with exactly the lines of 8.4.2 for that machine. `rc-attach`
  reads it through `XDG_CONFIG_HOME`, and `preflight` takes the other two
  machines from its `machine` lines (or from `--peers a,b`).
- Pane variables beyond the three of `rc-server --print-env`: one
  `NAME=value` per line in `~/.cache/devtools-rc/server.env`. On the mini
  that is the `SSH_AUTH_SOCK` of SY9.

## Order

1. `rc-install <ref>`
2. `rc-server start` [GO]
3. in a plain terminal window (no herdr, no tmux, no devtools):
   `rc-attach`. A candidate older than the cutover commit needs
   `rc-attach --practice`; such a pass is practice and does not count for
   G4 or G5. In a rehearsal pane the candidate is
   `~/.cache/devtools-rc/result/bin/devtools`; `devtools` on the PATH there
   is the installed one.
4. the matrix of 10.3; `rc-measure <alias> <label>` for each ordered pair
5. `rc-server stop` [GO]
6. on the day: `preflight` (CO1), and after the switch `postcheck` (CO7),
   then after CO8 once more in a devtools pane and once in a plain
   terminal

## How the checks read

One line per check, `<id> <verdict> <text>`, its details indented below.
Verdicts: `pass`, `FAIL`, `skip` (PC-9 without ssh to localhost), `n/a`
(PC-11 on macOS) and `left` (a check the other run of `postcheck` does).
PC-1 asks for `sessions` 0 only in a plain terminal while no client is
attached, the state of CO7.

## Self-tests

`preflight --self-test` and `postcheck --self-test` run every check against
fixtures: a new HOME under `/tmp`, a PATH of fakes and of links to a fixed
list of plain tools, and `DEVTOOLS_VERIFY_OS` to run the Linux and the
macOS branch on one machine. ssh, nix, herdr, systemctl, launchctl and the
installed devtools are fakes there, so a self-test reaches no host, no
service and no session. The one real program is tmux for PC-12, on the
socket `devtools-postcheck`; afterwards
`command tmux -L devtools-postcheck ls` fails because no server is left.

## Provisional

- PF-5 and PF-8 look for the per-user profile and for the tmux agent plist
  inside the built generation at places that are UNVERIFIED on macOS until
  P3-06 records them (SY18).
- `rc-measure` picks the measured values out of the `-v` output of
  `internal/e2e` by their threshold ids T1 to T10; the exact form of those
  lines is P3-05a's.
- PC-13 matches the `mismatch` words of `internal/ui/words.go` and the
  refusal of SY19 as text copied into `postcheck`; both are checked by the
  self-test only against that copy.

---
