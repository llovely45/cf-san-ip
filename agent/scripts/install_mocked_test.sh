#!/bin/sh
set -eu

# shellcheck disable=SC1091
. /etc/os-release

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPOSITORY_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
INSTALLER="$SCRIPT_DIR/install.sh"
SOURCE_FIXTURE_DIR=${CF_SAN_FIXTURE_DIR:-"$REPOSITORY_ROOT/test-fixtures"}
[ -x "$SOURCE_FIXTURE_DIR/cf-san" ] || { echo "missing Linux cf-san fixture: $SOURCE_FIXTURE_DIR/cf-san" >&2; exit 1; }
[ -x "$SOURCE_FIXTURE_DIR/zgrab2" ] || { echo "missing zgrab2 fixture" >&2; exit 1; }

TEMP_DIR=$(mktemp -d)
trap 'rm -rf "$TEMP_DIR"' 0
FIXTURE_DIR="$TEMP_DIR/release"
STUB_DIR="$TEMP_DIR/bin"
mkdir -p "$STUB_DIR" "$FIXTURE_DIR"
cp "$SOURCE_FIXTURE_DIR/cf-san" "$SOURCE_FIXTURE_DIR/zgrab2" "$SOURCE_FIXTURE_DIR/THIRD_PARTY_NOTICES.txt" "$FIXTURE_DIR/"
PACKAGE_LOG="$TEMP_DIR/package-manager.log"
CURL_LOG="$TEMP_DIR/curl.log"
export PACKAGE_LOG CURL_LOG CF_SAN_FIXTURE_DIR="$FIXTURE_DIR"

cat > "$STUB_DIR/apt-get" <<'STUB'
#!/bin/sh
printf 'apt-get %s\n' "$*" >> "$PACKAGE_LOG"
STUB
cat > "$STUB_DIR/apk" <<'STUB'
#!/bin/sh
printf 'apk %s\n' "$*" >> "$PACKAGE_LOG"
STUB
cat > "$STUB_DIR/zmap" <<'STUB'
#!/bin/sh
exit 0
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
  tar -czf "$archive" -C "$FIXTURE_DIR" cf-san zgrab2 THIRD_PARTY_NOTICES.txt
  sha256sum "$archive" > "$checksum"
  : > "$PACKAGE_LOG"
  : > "$CURL_LOG"
  install_output=$(CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" \
    --url 'https://worker.example' --token 'installer-test-secret' 2>&1) || {
      printf '%s\n' "$install_output" >&2
      exit 1
    }
  case "$install_output" in *installer-test-secret*) echo 'installer echoed token' >&2; exit 1 ;; esac
  [ -x /usr/local/bin/cf-san ] || { echo 'cf-san was not installed' >&2; exit 1; }
  [ -x /usr/local/bin/zgrab2 ] || { echo 'zgrab2 was not installed' >&2; exit 1; }
  [ "$(stat -c '%a' /etc/cf-san/config.json)" = 600 ] || { echo 'config permissions are not 0600' >&2; exit 1; }
  grep -q 'installer-test-secret' /etc/cf-san/config.json || { echo 'agent config omitted token' >&2; exit 1; }
  grep -q "cf-san_linux_${release_arch}.tar.gz" "$CURL_LOG" || { echo "installer selected the wrong release architecture ($release_arch)" >&2; exit 1; }
  case "${ID:-}" in
    debian) grep -q '^apt-get update$' "$PACKAGE_LOG"; grep -q 'apt-get install -y .*zmap' "$PACKAGE_LOG" ;;
    alpine) grep -q '^apk add --no-cache .*zmap' "$PACKAGE_LOG" ;;
    *) echo "unexpected container OS: ${ID:-unset}" >&2; exit 1 ;;
  esac

  /usr/local/bin/cf-san configure --url 'https://worker.example' --token 'old-token' --asn 16509 --limit 250 --senders 40
  : > "$PACKAGE_LOG"
  update_output=$(CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
    CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only 2>&1) || {
      printf '%s\n' "$update_output" >&2
      exit 1
    }
  case "$update_output" in *updated-token*) echo 'update installer echoed the API token' >&2; exit 1 ;; esac
  [ ! -s "$PACKAGE_LOG" ] || { echo 'update-only unexpectedly invoked a package manager' >&2; cat "$PACKAGE_LOG" >&2; exit 1; }
  grep -q '"url": "https://updated.example"' /etc/cf-san/config.json || { echo 'update did not save the new backend URL' >&2; exit 1; }
  grep -q '"token": "updated-token"' /etc/cf-san/config.json || { echo 'update did not save the new API token' >&2; exit 1; }
  grep -q '"asn": 16509' /etc/cf-san/config.json || { echo 'update did not preserve ASN' >&2; exit 1; }
  grep -q '"limit": 250' /etc/cf-san/config.json || { echo 'update did not preserve ZMap rate' >&2; exit 1; }
  grep -q '"senders": 40' /etc/cf-san/config.json || { echo 'update did not preserve ZGrab2 concurrency' >&2; exit 1; }

  rm -f /usr/local/bin/cf-san /usr/local/bin/zgrab2 /etc/cf-san/config.json
  rm -rf /etc/cf-san
  printf 'installer %s/%s install passed\n' "${ID:-linux}" "$release_arch"
done

export MOCK_ARCH=s390x
if CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --url 'https://worker.example' --token 'installer-test-secret' >"$TEMP_DIR/unsupported.out" 2>&1; then
  echo 'installer accepted unsupported architecture' >&2
  exit 1
fi
grep -q 'unsupported CPU architecture' "$TEMP_DIR/unsupported.out" || { cat "$TEMP_DIR/unsupported.out" >&2; exit 1; }

mkdir -p "$TEMP_DIR/bad-checksum"
cp "$FIXTURE_DIR/cf-san" "$FIXTURE_DIR/zgrab2" "$FIXTURE_DIR/THIRD_PARTY_NOTICES.txt" "$TEMP_DIR/bad-checksum/"
archive="$TEMP_DIR/bad-checksum/cf-san_linux_amd64.tar.gz"
tar -czf "$archive" -C "$TEMP_DIR/bad-checksum" cf-san zgrab2 THIRD_PARTY_NOTICES.txt
printf '%064d  %s\n' 0 "$archive" > "$archive.sha256"
CF_SAN_FIXTURE_DIR="$TEMP_DIR/bad-checksum" CF_SAN_RELEASE_REPOSITORY='example/cf-san' \
  PATH="$STUB_DIR:$PATH" MOCK_ARCH=x86_64 \
  sh "$INSTALLER" --url 'https://worker.example' --token 'installer-test-secret' >"$TEMP_DIR/bad-checksum.out" 2>&1 && {
    echo 'installer accepted checksum mismatch' >&2
    exit 1
  }
grep -q 'release checksum does not match' "$TEMP_DIR/bad-checksum.out" || { cat "$TEMP_DIR/bad-checksum.out" >&2; exit 1; }

printf 'installer mocked integration checks passed\n'
