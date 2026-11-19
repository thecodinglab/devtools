# shellcheck shell=sh
# shellcheck disable=SC2034 # the variables below are read by the scripts that source this file
#
# lib.sh: what the scripts of scripts/verify share (PLAN 9.2 to 9.5, P3-04).
# Sourced, never run. A script sets PROG and HERE before it sources it.

RC=${HOME:?HOME is not set}/.cache/devtools-rc # the rehearsal's own directory (9.2)
RC_BIN=$RC/result/bin/devtools                  # the candidate, built by rc-install
RC_SSH_DIR=$RC/bin                              # holds "ssh", a link to rc-ssh
RC_CONFIG=$RC/config                            # XDG_CONFIG_HOME of the rehearsal client
RC_SERVICE=devtools-rc                          # the unit or launchd label of rc-server
REAL_UNIT=devtools-server.service               # the managed server of 8.4.3
REAL_LABEL=org.nix-community.home.devtools-server
TMUX_LABEL=org.nix-community.home.tmux

say() { printf '%s\n' "$*"; }
warn() { printf '%s: %s\n' "$PROG" "$*" >&2; }
die() {
	warn "$*"
	exit 1
}
usage() {
	printf 'usage: %s\n' "$USAGE" >&2
	exit 2
}

# verify_os prints Linux or Darwin. DEVTOOLS_VERIFY_OS is set by the
# self-tests only, so that both branches run against fixtures on one machine.
verify_os() {
	_os=${DEVTOOLS_VERIFY_OS:-$(uname -s)}
	case $_os in
	Linux | Darwin) say "$_os" ;;
	*) die "unsupported system: $_os" ;;
	esac
}

# refuse_multiplexer_env stops a script that must run in a plain terminal
# (9.1): inside herdr, tmux or a devtools pane the candidate would use that
# backend and never start the rehearsal client (N18, N25).
refuse_multiplexer_env() {
	for _var in HERDR_ENV TMUX DEVTOOLS_PANE; do
		eval "_set=\${$_var+x}"
		if [ -n "$_set" ]; then
			die "refused: $_var is set, so this terminal is inside a multiplexer. Run it in a plain terminal window (no herdr, no tmux, no devtools)."
		fi
	done
}

# ensure_ssh_standin makes $RC_SSH_DIR/ssh a link to rc-ssh.
ensure_ssh_standin() {
	mkdir -p "$RC_SSH_DIR" || die "cannot create $RC_SSH_DIR"
	ln -sf "$HERE/rc-ssh" "$RC_SSH_DIR/ssh" || die "cannot link $RC_SSH_DIR/ssh"
}

# shquote prints one word so that a shell reads it back unchanged.
shquote() {
	case $1 in
	'') printf "''" ;;
	*[!A-Za-z0-9_./=:,@%+-]*) printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")" ;;
	*) printf '%s' "$1" ;;
	esac
}

# print_cmd prints its arguments as one command line.
print_cmd() {
	_sep=
	for _word do
		printf '%s' "$_sep"
		shquote "$_word"
		_sep=' '
	done
	printf '\n'
}

# file_mtime prints the modification time of a path itself (a symlink is
# not followed), in seconds; GNU stat first, then BSD stat.
file_mtime() { stat -c %Y "$1" 2>/dev/null || stat -f %m "$1" 2>/dev/null; }

# file_mode_uid prints "<octal mode> <owner uid>" of a path.
file_mode_uid() { stat -c '%a %u' "$1" 2>/dev/null || stat -f '%Lp %u' "$1" 2>/dev/null; }

# Store paths: NIX_STORE_DIR is Nix's own variable; the self-tests point it
# at their fixtures.
STORE=${NIX_STORE_DIR:-/nix/store}

# Check lines. A check calls note for its details and then check once:
#   PF-3   pass  ./result is newer than the commit
# Its details follow, indented. FAILED counts the failed checks; the other
# verdicts are pass, skip, n/a and left (to the other run of postcheck).
# WORK is a scratch directory removed at exit.
FAILED=0
begin_checks() {
	WORK=$(mktemp -d "${TMPDIR:-/tmp}/devtools-verify.XXXXXX") || die "mktemp failed"
	DETAILS=$WORK/details
	: >"$DETAILS"
	trap 'rm -rf "$WORK"' EXIT
	trap 'exit 1' HUP INT TERM
}
note() { printf '%s\n' "$*" >>"$DETAILS"; }
note_output() { sed 's/^/  | /' >>"$DETAILS"; } # reads the output of a command
check() {
	_id=$1 _verdict=$2
	shift 2
	printf '%-6s %-5s %s\n' "$_id" "$_verdict" "$*"
	[ "$_verdict" = FAIL ] && FAILED=$((FAILED + 1))
	sed 's/^/         /' "$DETAILS"
	: >"$DETAILS"
}

# Self-tests (preflight --self-test, postcheck --self-test). They run the
# script in a child with a complete, made-up environment: HOME is a new
# directory under /tmp, and PATH holds only fakes and links to a fixed list
# of harmless tools. A command the child would need from the machine (ssh,
# nix, herdr, systemctl, launchctl, the installed devtools, ...) is a fake
# that answers from a fixture file or fails, so a self-test reaches no host,
# no service, no herdr and no tmux server of the user. The one real program
# it runs besides the tools is tmux, and only on the socket
# devtools-postcheck (PC-12).
st_begin() {
	ST=$(mktemp -d /tmp/devtools-verify.XXXXXX) || die "self-test: mktemp failed"
	trap 'st_end' EXIT
	trap 'exit 1' HUP INT TERM
	mkdir -p "$ST/safe" "$ST/fake" "$ST/fx" "$ST/home" "$ST/store" || die "self-test: mkdir failed"
	for _tool in sh cat sed grep awk tr head tail wc sort uniq cut ls stat readlink \
		mkdir rm ln chmod touch date mktemp cmp id uname dirname basename env git jq mv cp; do
		_path=$(command -v "$_tool" 2>/dev/null) || _path=
		case $_path in
		/*) ln -s "$_path" "$ST/safe/$_tool" || die "self-test: ln failed" ;;
		*) die "self-test: needs $_tool on PATH" ;;
		esac
	done
	ST_ENV=$(command -v env)
	ST_SH=$(command -v sh)
	ST_USER=$(id -un)
	ST_UID=$(id -u)
	ST_RUNS=0 ST_FAILS=0
	cat >"$ST/fake/fake-command" <<'EOF'
#!/bin/sh
# A fake of a self-test (scripts/verify/lib.sh): answers from a fixture file.
name=${0##*/}
fx=$DEVTOOLS_VERIFY_FX
printf '%s %s\n' "$name" "$*" >>"$fx/calls.log"
case $name in
tmux)
	# the private server of PC-12 is the one real tmux of a self-test
	if [ "${1-}" = -L ] && [ "${2-}" = devtools-postcheck ]; then exec "$DEVTOOLS_VERIFY_REAL_TMUX" "$@"; fi ;;
nix-store)
	if [ "${1-}" = --realise ] && [ "${3-}" = --add-root ]; then ln -s "$2" "$4" && printf '%s\n' "$4"; exit; fi ;;
esac
key=$(printf '%s' "$name $*" | tr -c 'A-Za-z0-9._-' '_')
if [ -f "$fx/$key.out" ]; then
	cat "$fx/$key.out"
	exit "$(cat "$fx/$key.rc")"
fi
printf 'fake %s: no fixture for: %s\n' "$name" "$*" >&2
exit 127
EOF
	chmod +x "$ST/fake/fake-command" || die "self-test: chmod failed"
}

# st_end removes the sandbox; ST_CLEANUP names a function to run first.
st_end() {
	if [ -n "${ST_CLEANUP-}" ]; then "$ST_CLEANUP"; fi
	rm -rf "$ST"
}

# st_fakes <name>... puts fakes of these commands on the child's PATH.
st_fakes() {
	for _name do
		ln -sf fake-command "$ST/fake/$_name" || die "self-test: ln failed"
	done
}

# fx <command> <arguments as one string> <output> [<exit status>] writes the
# answer of a fake to one call.
fx() {
	_key=$(printf '%s' "$1 $2" | tr -c 'A-Za-z0-9._-' '_')
	if [ -n "$3" ]; then printf '%s\n' "$3" >"$ST/fx/$_key.out"; else : >"$ST/fx/$_key.out"; fi
	printf '%s\n' "${4:-0}" >"$ST/fx/$_key.rc"
}

# st_run <want> <label> <pattern> <NAME=value>... <program> <argument>...
# runs one scenario in the made-up environment. want is pass (exit 0) or
# fail (exit not 0); pattern is an extended regular expression the output
# must match, or "-". A call to a fake without a fixture fails the scenario.
st_run() {
	_want=$1 _label=$2 _pattern=$3
	shift 3
	ST_RUNS=$((ST_RUNS + 1))
	: >"$ST/fx/calls.log"
	"$ST_ENV" -i HOME="$ST/home" PATH="$ST/fake:$ST/safe" LANG=C USER="$ST_USER" LOGNAME="$ST_USER" \
		SHELL=/bin/sh NIX_STORE_DIR="$ST/store" GIT_CONFIG_NOSYSTEM=1 \
		DEVTOOLS_VERIFY_FX="$ST/fx" DEVTOOLS_VERIFY_REAL_TMUX="${ST_TMUX-}" \
		TMUX_TMPDIR="${TMUX_TMPDIR:-/tmp}" "$@" >"$ST/out" 2>&1
	_rc=$?
	_why=
	case $_want in
	pass) [ "$_rc" -eq 0 ] || _why="exit $_rc, want 0" ;;
	fail) [ "$_rc" -ne 0 ] || _why="exit 0, want a failure" ;;
	esac
	if [ "$_pattern" != - ] && ! grep -Eq -- "$_pattern" "$ST/out"; then
		_why="${_why:+$_why; }output does not match /$_pattern/"
	fi
	if grep -q '^fake .*: no fixture for:' "$ST/out"; then
		_why="${_why:+$_why; }a fake was called without a fixture"
	fi
	if [ -z "$_why" ]; then
		say "self-test: pass  $_label"
		grep -v '^[[:space:]]' "$ST/out" | sed 's/^/    /' # the check lines, without details
	else
		say "self-test: FAIL  $_label: $_why"
		ST_FAILS=$((ST_FAILS + 1))
		sed 's/^/    /' "$ST/out"
	fi
}

# st_finish prints the verdict and exits with it.
st_finish() {
	if [ "$ST_FAILS" -eq 0 ]; then
		say "self-test: all $ST_RUNS scenarios passed"
		exit 0
	fi
	say "self-test: $ST_FAILS of $ST_RUNS scenarios failed"
	exit 1
}
