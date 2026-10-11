#!/bin/sh
set -eu

# shellcheck disable=SC1091
. /etc/os-release

case "${ID:-}" in
  debian)
    if [ "${VERSION_ID%%.*}" -ge 13 ]; then
      MOCK_PCAP_PACKAGE=libpcap0.8t64
    else
      MOCK_PCAP_PACKAGE=libpcap0.8
    fi
    export MOCK_PCAP_PACKAGE
    ;;
esac

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPOSITORY_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
INSTALLER="$SCRIPT_DIR/install.sh"
SOURCE_FIXTURE_DIR=${CF_SAN_FIXTURE_DIR:-"$REPOSITORY_ROOT/test-fixtures"}
[ -x "$SOURCE_FIXTURE_DIR/cf-san" ] || { echo "missing Linux cf-san fixture: $SOURCE_FIXTURE_DIR/cf-san" >&2; exit 1; }

TEMP_DIR=$(mktemp -d)
trap 'rm -rf "$TEMP_DIR"' 0
FIXTURE_DIR="$TEMP_DIR/release"
STUB_DIR="$TEMP_DIR/bin"
mkdir -p "$STUB_DIR" "$FIXTURE_DIR"
cp "$SOURCE_FIXTURE_DIR/cf-san" "$FIXTURE_DIR/"
PACKAGE_LOG="$TEMP_DIR/package-manager.log"
CURL_LOG="$TEMP_DIR/curl.log"
export PACKAGE_LOG CURL_LOG CF_SAN_FIXTURE_DIR="$FIXTURE_DIR" STUB_DIR

create_masscan_stub() {
  cat > "$STUB_DIR/masscan" <<'STUB'
#!/bin/sh
exit 0
STUB
  chmod +x "$STUB_DIR/masscan"
}

cat > "$STUB_DIR/apt-get" <<'STUB'
#!/bin/sh
set -eu
printf 'apt-get %s\n' "$*" >> "$PACKAGE_LOG"
case "$*" in *masscan*)
  cat > "$STUB_DIR/masscan" <<'MASSCAN'
#!/bin/sh
exit 0
MASSCAN
  chmod +x "$STUB_DIR/masscan"
  ;;
esac
STUB
cat > "$STUB_DIR/apt-cache" <<'STUB'
#!/bin/sh
set -eu
printf 'apt-cache %s\n' "$*" >> "$PACKAGE_LOG"
if [ "${1:-}" = show ] && [ "${2:-}" = "${MOCK_PCAP_PACKAGE:-}" ]; then
  printf 'Package: %s\n' "$MOCK_PCAP_PACKAGE"
  exit 0
fi
exit 1
STUB
cat > "$STUB_DIR/apk" <<'STUB'
#!/bin/sh
set -eu
printf 'apk %s\n' "$*" >> "$PACKAGE_LOG"
case "$*" in *masscan*)
  cat > "$STUB_DIR/masscan" <<'MASSCAN'
#!/bin/sh
exit 0
MASSCAN
  chmod +x "$STUB_DIR/masscan"
  ;;
esac
STUB
cat > "$STUB_DIR/uname" <<'STUB'
#!/bin/sh
if [ "${1:-}" = "-m" ]; then
  printf '%s\n' "${MOCK_ARCH:-x86_64}"
else
  exec /usr/bin/uname "$@"
fi
STUB
cat > "$STUB_DIR/curl" <<'STUB'
#!/bin/sh
set -eu
output=''
last=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      [ "$#" -ge 2 ] || exit 2
      output=$2
      shift 2
      ;;
    *)
      last=$1
      shift
      ;;
  esac
done
[ -n "$output" ] || exit 2
name=${last##*/}
printf '%s\n' "$last" >> "$CURL_LOG"
cp "$CF_SAN_FIXTURE_DIR/$name" "$output"
STUB
chmod +x "$STUB_DIR"/*
export PATH="$STUB_DIR:$PATH"

for arch_case in 'x86_64 amd64' 'aarch64 arm64'; do
  set -- $arch_case
  mock_arch=$1
  release_arch=$2
  export MOCK_ARCH=$mock_arch
  archive="$FIXTURE_DIR/cf-san_linux_${release_arch}.tar.gz"
  checksum="$archive.sha256"
  tar -czf "$archive" -C "$FIXTURE_DIR" cf-san
  sha256sum "$archive" > "$checksum"
  : > "$PACKAGE_LOG"
  : > "$CURL_LOG"
  rm -f "$STUB_DIR/masscan"
  # Simulate a previous release's bundled files; the new installer should remove
  # the old ZGrab2 binary but leave system-managed packages alone.
  install -d /usr/local/bin /usr/local/share/cf-san
  printf '#!/bin/sh\nexit 0\n' > /usr/local/bin/zgrab2
  chmod +x /usr/local/bin/zgrab2
  printf 'old bundle\n' > /usr/local/share/cf-san/zgrab2-blocklist.conf

  install_output=$(CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" \
    --url 'https://worker.example' --token 'installer-test-secret' 2>&1) || {
      printf '%s\n' "$install_output" >&2
      exit 1
    }
  case "$install_output" in *installer-test-secret*) echo 'installer echoed token' >&2; exit 1 ;; esac
  [ -x /usr/local/bin/cf-san ] || { echo 'cf-san was not installed' >&2; exit 1; }
  command -v masscan >/dev/null 2>&1 || { echo 'masscan was not installed' >&2; exit 1; }
  [ ! -e /usr/local/bin/zgrab2 ] || { echo 'legacy zgrab2 binary was not removed' >&2; exit 1; }
  [ ! -e /usr/local/share/cf-san/zgrab2-blocklist.conf ] || { echo 'legacy bundled blocklist was not removed' >&2; exit 1; }
  [ "$(stat -c '%a' /etc/cf-san/config.json)" = 600 ] || { echo 'config permissions are not 0600' >&2; exit 1; }
  grep -q 'installer-test-secret' /etc/cf-san/config.json || { echo 'agent config omitted token' >&2; exit 1; }
  grep -q "cf-san_linux_${release_arch}.tar.gz" "$CURL_LOG" || { echo "installer selected the wrong release architecture ($release_arch)" >&2; exit 1; }
  case "${ID:-}" in
    debian)
      grep -q '^apt-get update$' "$PACKAGE_LOG"
      grep -q 'apt-get install -y .*masscan' "$PACKAGE_LOG"
      grep -q "apt-get install -y .*${MOCK_PCAP_PACKAGE}" "$PACKAGE_LOG"
      ;;
    alpine) grep -q '^apk add --no-cache .*masscan' "$PACKAGE_LOG" ;;
    *) echo "unexpected container OS: ${ID:-unset}" >&2; exit 1 ;;
  esac
  if grep -q 'zmap' "$PACKAGE_LOG"; then echo 'installer still installs ZMap' >&2; exit 1; fi

  /usr/local/bin/cf-san configure --url 'https://worker.example' --token 'old-token' --asn 16509 --limit 250 --workers 40
  : > "$PACKAGE_LOG"
  rm -f "$STUB_DIR/masscan"
  update_output=$(CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
    CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only 2>&1) || {
      printf '%s\n' "$update_output" >&2
      exit 1
    }
  case "$update_output" in *updated-token*) echo 'update installer echoed the API token' >&2; exit 1 ;; esac
  command -v masscan >/dev/null 2>&1 || { echo 'update-only did not install missing masscan' >&2; exit 1; }
  case "${ID:-}" in
    debian)
      grep -q '^apt-get update$' "$PACKAGE_LOG"
      grep -q 'apt-get install -y masscan' "$PACKAGE_LOG"
      grep -q "apt-get install -y .*${MOCK_PCAP_PACKAGE}" "$PACKAGE_LOG"
      ;;
    alpine) grep -q '^apk add --no-cache masscan$' "$PACKAGE_LOG" ;;
  esac
  grep -q '"url": "https://updated.example"' /etc/cf-san/config.json || { echo 'update did not save the new backend URL' >&2; exit 1; }
  grep -q '"token": "updated-token"' /etc/cf-san/config.json || { echo 'update did not save the new API token' >&2; exit 1; }
  grep -q '"asn": 16509' /etc/cf-san/config.json || { echo 'update did not preserve ASN' >&2; exit 1; }
  grep -q '"limit": 250' /etc/cf-san/config.json || { echo 'update did not preserve Masscan rate' >&2; exit 1; }
  grep -q '"curlWorkers": 40' /etc/cf-san/config.json || { echo 'update did not preserve curl validation concurrency' >&2; exit 1; }

  # Upgrading from the ZMap/ZGrab2 agent may encounter a legacy sender count
  # above the new curl worker ceiling. The replacement binary must migrate and
  # persist a usable capped value instead of failing configuration.
  printf '%s\n' '{"url":"https://updated.example","token":"updated-token","asn":16509,"limit":250,"senders":8192,"agentId":"legacy-agent"}' > /etc/cf-san/config.json
  legacy_update_output=$(CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
    CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only 2>&1) || {
      printf '%s\n' "$legacy_update_output" >&2
      exit 1
    }
  case "$legacy_update_output" in *'curl worker count cannot exceed'*) echo 'update rejected an over-limit legacy sender count' >&2; exit 1 ;; esac
  grep -q '"curlWorkers": 4096' /etc/cf-san/config.json || { echo 'update did not cap legacy sender count at 4096' >&2; exit 1; }
  if grep -q '"senders"' /etc/cf-san/config.json; then echo 'update did not migrate the legacy senders key' >&2; exit 1; fi
  grep -q '"agentId": "legacy-agent"' /etc/cf-san/config.json || { echo 'update did not preserve the legacy Agent ID' >&2; exit 1; }

  # Existing Masscan installations may still be missing libpcap on Debian
  # Bookworm. Updating cf-san must repair that dependency even when the binary
  # is already present.
  : > "$PACKAGE_LOG"
  create_masscan_stub
  existing_update_output=$(CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
    CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only 2>&1) || {
      printf '%s\n' "$existing_update_output" >&2
      exit 1
    }
  case "${ID:-}" in
    debian)
      grep -q "apt-get install -y ${MOCK_PCAP_PACKAGE}" "$PACKAGE_LOG" || { echo 'update did not repair the missing Masscan libpcap dependency' >&2; exit 1; }
      if grep -q 'apt-get install -y masscan' "$PACKAGE_LOG"; then echo 'update reinstalled an existing Masscan binary unnecessarily' >&2; exit 1; fi
      ;;
    alpine)
      [ ! -s "$PACKAGE_LOG" ] || { echo 'Alpine updated packages unnecessarily when Masscan was already present' >&2; exit 1; }
      ;;
  esac

  rm -f /usr/local/bin/cf-san /usr/local/bin/zgrab2 /etc/cf-san/config.json "$STUB_DIR/masscan"
  rm -rf /etc/cf-san /usr/local/share/cf-san
  printf 'installer %s/%s install passed\n' "${ID:-linux}" "$release_arch"
done

export MOCK_ARCH=s390x
if CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --url 'https://worker.example' --token 'installer-test-secret' >"$TEMP_DIR/unsupported.out" 2>&1; then
  echo 'installer accepted unsupported architecture' >&2
  exit 1
fi
grep -q 'unsupported CPU architecture' "$TEMP_DIR/unsupported.out" || { cat "$TEMP_DIR/unsupported.out" >&2; exit 1; }

mkdir -p "$TEMP_DIR/bad-checksum"
cp "$FIXTURE_DIR/cf-san" "$TEMP_DIR/bad-checksum/"
archive="$TEMP_DIR/bad-checksum/cf-san_linux_amd64.tar.gz"
tar -czf "$archive" -C "$TEMP_DIR/bad-checksum" cf-san
printf '%064d  %s\n' 0 "$archive" > "$archive.sha256"
CF_SAN_FIXTURE_DIR="$TEMP_DIR/bad-checksum" CF_SAN_RELEASE_REPOSITORY='example/cf-san' \
  PATH="$STUB_DIR:$PATH" MOCK_ARCH=x86_64 \
  sh "$INSTALLER" --url 'https://worker.example' --token 'installer-test-secret' >"$TEMP_DIR/bad-checksum.out" 2>&1 && {
    echo 'installer accepted checksum mismatch' >&2
    exit 1
  }
grep -q 'release checksum does not match' "$TEMP_DIR/bad-checksum.out" || { cat "$TEMP_DIR/bad-checksum.out" >&2; exit 1; }

printf 'installer mocked integration checks passed\n'
