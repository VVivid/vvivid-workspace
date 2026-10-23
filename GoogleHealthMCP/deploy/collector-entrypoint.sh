#!/bin/sh
set -eu
umask 077

fail() {
    printf '%s\n' '{"status":"error","error_code":"sqlite_scratch_unavailable"}' >&2
    exit 1
}

# Large final-page transactions can exceed the container's 16 MiB /tmp.
# Keep SQLite spill files beside its persistent database, with private access.
SQLITE_TMPDIR=${SQLITE_TMPDIR:-/data/scratch}
export SQLITE_TMPDIR
[ ! -L "$SQLITE_TMPDIR" ] || fail
mkdir -p "$SQLITE_TMPDIR" 2>/dev/null || fail
chmod 700 "$SQLITE_TMPDIR" 2>/dev/null || fail
probe=$(mktemp "$SQLITE_TMPDIR/.writable.XXXXXX" 2>/dev/null) || fail
rm -f "$probe" 2>/dev/null || fail

exec /usr/local/bin/google-health "$@"
