#!/bin/sh
set -eu

EVENT_NAME=${1:-}
REF_TYPE=${2:-}
REF_NAME=${3:-}

fail() {
  printf 'release workflow: %s\n' "$1" >&2
  exit 1
}

# Manual dispatch is intentionally allowed from a branch so the verification
# and installer-matrix jobs remain useful without publishing a release.
if [ "$EVENT_NAME" = workflow_dispatch ]; then
  exit 0
fi

[ "$EVENT_NAME" = push ] || fail 'unsupported workflow event'
[ "$REF_TYPE" = tag ] || fail 'push workflow must target a tag'
printf '%s\n' "$REF_NAME" | LC_ALL=C grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || \
  fail 'release tag must be exactly vMAJOR.MINOR.PATCH without prerelease or build metadata'
