#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
INSTALLER="$SCRIPT_DIR/install.sh"

fail() {
  printf 'installer test failed: %s\n' "$1" >&2
  exit 1
}

sh -n "$INSTALLER" || fail 'installer has invalid POSIX shell syntax'
bash -n "$INSTALLER" || fail 'installer has invalid Bash-compatible syntax'

HELP_OUTPUT=$(sh "$INSTALLER" --help 2>&1) || fail '--help did not succeed'
printf '%s' "$HELP_OUTPUT" | grep -q 'Usage:' || fail '--help omitted usage'
printf '%s' "$HELP_OUTPUT" | grep -q -- '--update-only' || fail '--help omitted update-only mode'

expect_failure() {
  expected=$1
  shift
  if OUTPUT=$(CF_SAN_RELEASE_REPOSITORY='invalid repo' sh "$INSTALLER" "$@" 2>&1); then
    fail "installer unexpectedly succeeded for: $*"
  fi
  printf '%s' "$OUTPUT" | grep -Fq "$expected" || fail "expected '$expected' in failure output"
}

expect_failure 'backend URL is required'
expect_failure 'backend URL must use HTTPS' --url http://worker.example --token 'token-must-not-leak'
expect_failure 'API token is required' --url https://worker.example
expect_failure 'release repository is not configured on the Worker' --url https://worker.example --token 'token-must-not-leak'

if printf '%s' "$OUTPUT" | grep -q 'token-must-not-leak'; then
  fail 'installer echoed the API token in an error message'
fi

printf 'installer preflight checks passed\n'
