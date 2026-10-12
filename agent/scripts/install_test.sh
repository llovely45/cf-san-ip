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
  printf '%s' "$OUTPUT" | grep -Fq -- "$expected" || fail "expected '$expected' in failure output"
}

expect_failure 'backend URL is required'
expect_failure 'backend URL must use HTTPS' --url http://worker.example --token 'token-must-not-leak'
expect_failure 'API token is required' --url https://worker.example
expect_failure 'release repository is not configured on the Worker' --url https://worker.example --token 'token-must-not-leak'

# A valid pin is accepted only in update-only mode. The deliberately invalid
# repository makes the accepted case stop before any package or release work.
expect_failure 'release repository is not configured on the Worker' \
  --url https://worker.example --token 'token-must-not-leak' --update-only --version v1.2.3
expect_failure '--version requires --update-only' \
  --url https://worker.example --token 'token-must-not-leak' --version v1.2.3
expect_failure 'missing value for --version' \
  --url https://worker.example --token 'token-must-not-leak' --update-only --version
expect_failure 'missing value for --version' \
  --url https://worker.example --token 'token-must-not-leak' --update-only --version ''

for version in latest v1.2.3v v1.2.3suffix v1.2.3-rc.1 v1.02.3 v1.2 v1.2.3+build; do
  expect_failure 'version must be a stable vMAJOR.MINOR.PATCH tag' \
    --url https://worker.example --token 'token-must-not-leak' --update-only --version "$version"
done

embedded_newline_version=$(printf 'v1.2.3\ninvalid')
expect_failure 'version must be a stable vMAJOR.MINOR.PATCH tag' \
  --url https://worker.example --token 'token-must-not-leak' --update-only --version "$embedded_newline_version"
embedded_control_version=$(printf 'v1.2.3\tinvalid')
expect_failure 'version must be a stable vMAJOR.MINOR.PATCH tag' \
  --url https://worker.example --token 'token-must-not-leak' --update-only --version "$embedded_control_version"

# Keep the URL selection and checksum download paths visible in this focused
# preflight test; the mocked integration test exercises the real downloads.
grep -Fq 'releases/latest/download' "$INSTALLER" || fail 'installer lost latest-release URL'
grep -Fq 'releases/download/${UPDATE_VERSION}' "$INSTALLER" || fail 'installer lost pinned-release URL'

if printf '%s' "$OUTPUT" | grep -q 'token-must-not-leak'; then
  fail 'installer echoed the API token in an error message'
fi

printf 'installer preflight checks passed\n'
