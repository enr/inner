#!/usr/bin/env bash
# Signal and terminal probe for the manual TUI checklist.
#
# It stands in for a real TUI in the one respect that matters for the network
# relay: how signals and terminal state reach the entrypoint. Unlike claude or
# gemini it needs no credentials, no network and no upstream version pinning,
# and it says exactly what it received instead of leaving you to judge whether
# a rendered UI "looked right".
#
# Run it through both profiles in profiles/ and compare. See README.md.

set -u

SIGINT_COUNT=0
SIGWINCH_COUNT=0
SIGTERM_COUNT=0

on_int()   { SIGINT_COUNT=$((SIGINT_COUNT + 1));   printf '\r\n>>> SIGINT   received (total: %d)\r\n' "$SIGINT_COUNT"; }
on_winch() { SIGWINCH_COUNT=$((SIGWINCH_COUNT + 1)); printf '\r\n>>> SIGWINCH received (total: %d) — new size: %sx%s\r\n' "$SIGWINCH_COUNT" "$(tput cols 2>/dev/null || echo '?')" "$(tput lines 2>/dev/null || echo '?')"; }
on_term()  { SIGTERM_COUNT=$((SIGTERM_COUNT + 1));  printf '\r\n>>> SIGTERM  received (total: %d) — exiting\r\n' "$SIGTERM_COUNT"; exit 0; }

trap on_int   INT
trap on_winch WINCH
trap on_term  TERM

printf '\r\n── inner signal/terminal probe ──\r\n\r\n'

# pid/ppid only. Process group and session are deliberately NOT printed: under
# --unshare-pid procps resolves them to 0 because the leader is not visible in
# this namespace, so comparing them between runs would prove nothing. What
# actually proves the child shares the relay's foreground process group is
# SIGINT arriving at all — if it did not, the tty would not be delivering to it.
printf 'process:  pid=%s ppid=%s\r\n' "$$" "$PPID"

# With the relay in the chain there is one more process between bwrap and here.
printf 'parent:   %s\r\n' "$(ps -o args= -p "$PPID" 2>/dev/null | cut -c1-70 || echo '?')"

if exec 3<>/dev/tty 2>/dev/null; then
    printf 'tty:      /dev/tty is openable (controlling terminal intact)\r\n'
    exec 3>&-
else
    printf 'tty:      /dev/tty CANNOT be opened — a real TUI would hang here\r\n'
fi

printf 'termios:  %s\r\n' "$(stty -g 2>/dev/null | cut -c1-40 || echo 'stty unavailable')"
printf 'size:     %sx%s\r\n' "$(tput cols 2>/dev/null || echo '?')" "$(tput lines 2>/dev/null || echo '?')"
printf 'proxy:    HTTPS_PROXY=%s\r\n' "${HTTPS_PROXY:-<unset>}"
printf '/proc:    %s numeric entries\r\n' "$(ls /proc 2>/dev/null | grep -c '^[0-9]\+$' || echo '?')"

printf '\r\nNow exercise the terminal:\r\n'
printf '  Ctrl-C          → expect EXACTLY ONE "SIGINT received" per keypress\r\n'
printf '  resize window   → expect "SIGWINCH received" with the new size\r\n'
printf '  type + Enter    → expect the line echoed back\r\n'
printf '  q + Enter       → quit\r\n\r\n'

while true; do
    if read -r -t 1 line; then
        case "$line" in
            q|quit|exit) printf '\r\nbye — SIGINT:%d SIGWINCH:%d\r\n' "$SIGINT_COUNT" "$SIGWINCH_COUNT"; exit 0 ;;
            *)           printf 'echo: %s\r\n' "$line" ;;
        esac
    fi
done
