#!/bin/sh
set -eu

DEFAULT_RELEASE_REPOSITORY='__CF_SAN_RELEASE_REPOSITORY__'
BACKEND_URL=''
API_TOKEN=''

fail() {
  printf 'cf-san installer: %s\n' "$1" >&2
  exit 1
}

usage() {
  printf 'Usage: install.sh --url https://worker.example --token TOKEN\n' >&2
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --url)
      [ "$#" -ge 2 ] || { usage; fail 'missing value for --url'; }
      BACKEND_URL=$2
      shift 2
      ;;
    --token)
      [ "$#" -ge 2 ] || { usage; fail 'missing value for --token'; }
      API_TOKEN=$2
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      usage
      fail 'unknown argument'
      ;;
  esac
done

[ -n "$BACKEND_URL" ] || { usage; fail 'backend URL is required'; }
case "$BACKEND_URL" in https://*) ;; *) fail 'backend URL must use HTTPS' ;; esac
[ -n "$API_TOKEN" ] || { usage; fail 'API token is required'; }

RELEASE_REPOSITORY=${CF_SAN_RELEASE_REPOSITORY:-$DEFAULT_RELEASE_REPOSITORY}
printf '%s\n' "$RELEASE_REPOSITORY" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || \
  fail 'release repository is not configured on the Worker'

[ "$(id -u)" -eq 0 ] || fail 'run this script with sudo or as root'
[ -r /etc/os-release ] || fail 'cannot identify Linux distribution'
# shellcheck disable=SC1091
. /etc/os-release
case "${ID:-}" in
  debian)
    command -v apt-get >/dev/null 2>&1 || fail 'apt-get is required on Debian'
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl coreutils tar gzip zmap
    ;;
  alpine)
    command -v apk >/dev/null 2>&1 || fail 'apk is required on Alpine'
    apk add --no-cache ca-certificates curl coreutils tar gzip zmap
    ;;
  *)
    fail "unsupported Linux distribution: ${ID:-unknown}; supported: Debian and Alpine"
    ;;
esac

command -v zmap >/dev/null 2>&1 || fail 'ZMap installation failed; install the zmap package and retry'

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) fail "unsupported CPU architecture: $(uname -m); supported: amd64 and arm64" ;;
esac

TEMP_DIR=$(mktemp -d)
cleanup() { rm -rf -- "$TEMP_DIR"; }
trap cleanup 0
ARCHIVE="cf-san_linux_${ARCH}.tar.gz"
BASE_URL="https://github.com/${RELEASE_REPOSITORY}/releases/latest/download"
curl --fail --silent --show-error --location --retry 3 "${BASE_URL}/${ARCHIVE}" -o "${TEMP_DIR}/${ARCHIVE}"
curl --fail --silent --show-error --location --retry 3 "${BASE_URL}/${ARCHIVE}.sha256" -o "${TEMP_DIR}/${ARCHIVE}.sha256"
EXPECTED_SHA=$(awk 'NR == 1 { print $1 }' "${TEMP_DIR}/${ARCHIVE}.sha256")
printf '%s\n' "$EXPECTED_SHA" | grep -Eq '^[A-Fa-f0-9]{64}$' || fail 'release checksum file is invalid'
ACTUAL_SHA=$(sha256sum "${TEMP_DIR}/${ARCHIVE}" | awk '{ print $1 }')
[ "$ACTUAL_SHA" = "$EXPECTED_SHA" ] || fail 'release checksum does not match'

tar -xzf "${TEMP_DIR}/${ARCHIVE}" -C "$TEMP_DIR"
[ -x "${TEMP_DIR}/cf-san" ] || fail 'release archive does not contain cf-san'
[ -x "${TEMP_DIR}/zgrab2" ] || fail 'release archive does not contain zgrab2'
install -m 0755 "${TEMP_DIR}/cf-san" /usr/local/bin/cf-san
install -m 0755 "${TEMP_DIR}/zgrab2" /usr/local/bin/zgrab2
if [ -f "${TEMP_DIR}/THIRD_PARTY_NOTICES.txt" ]; then
  install -m 0644 "${TEMP_DIR}/THIRD_PARTY_NOTICES.txt" /usr/local/share/cf-san-THIRD_PARTY_NOTICES.txt
fi

/usr/local/bin/cf-san configure --url "$BACKEND_URL" --token "$API_TOKEN"
printf 'cf-san installed. Run `cf-san` to initialize settings and open the text menu.\n'
