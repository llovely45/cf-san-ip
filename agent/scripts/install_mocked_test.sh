#!/bin/sh
set -eu

fail() {
  printf 'installer mocked test failed: %s\n' "$1" >&2
  exit 1
}

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPOSITORY_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)

if [ "${CF_SAN_MOCKED_TEST_IN_CONTAINER:-0}" != 1 ]; then
  command -v docker >/dev/null 2>&1 || fail 'Docker is required for the Debian/Alpine matrix'
  for distro_image in \
    debian:bookworm-slim \
    debian:trixie-slim \
    alpine:3.18 \
    alpine:3.22 \
    alpine:3.23; do
    printf 'running mocked installer matrix: %s\n' "$distro_image"
    if ! docker run --rm --network none \
      -v "$REPOSITORY_ROOT:/workspace:ro" \
      -e CF_SAN_MOCKED_TEST_IN_CONTAINER=1 \
      "$distro_image" /bin/sh /workspace/agent/scripts/install_mocked_test.sh; then
      fail "mocked installer matrix failed for $distro_image"
    fi
  done
  printf 'installer mocked distro matrix passed\n'
  exit 0
fi

# shellcheck disable=SC1091
. /etc/os-release
case "${ID:-}" in
  debian|alpine) ;;
  *) fail "unexpected test container OS: ${ID:-unset}" ;;
esac

INSTALLER="$SCRIPT_DIR/install.sh"
SOURCE_FIXTURE_DIR=${CF_SAN_FIXTURE_DIR:-}
[ -x "$INSTALLER" ] || fail "missing installer: $INSTALLER"

TEMP_DIR=$(mktemp -d)
trap 'rm -rf "$TEMP_DIR"' 0
FIXTURE_DIR="$TEMP_DIR/release"
STUB_DIR="$TEMP_DIR/bin"
SOURCE_FIXTURE_DIR=${SOURCE_FIXTURE_DIR:-"$TEMP_DIR/source-fixture"}
mkdir -p "$STUB_DIR" "$FIXTURE_DIR" "$SOURCE_FIXTURE_DIR"

if [ -z "${CF_SAN_FIXTURE_DIR:-}" ]; then
  cat > "$SOURCE_FIXTURE_DIR/cf-san" <<'FIXTURE'
#!/bin/sh
set -eu

command_name=${1:-}
shift || true
case "$command_name" in
  configure)
    url=
    token=
    asn=0
    limit=0
    workers=0
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --url) url=$2; shift 2 ;;
        --token) token=$2; shift 2 ;;
        --asn) asn=$2; shift 2 ;;
        --limit) limit=$2; shift 2 ;;
        --workers) workers=$2; shift 2 ;;
        *) shift ;;
      esac
    done
    config_path=${CF_SAN_CONFIG_PATH:-/etc/cf-san/config.json}
    config_dir=$(dirname "$config_path")
    mkdir -p "$config_dir"
    umask 077
    cat > "$config_path" <<CONFIG
{
  "url": "$url",
  "token": "$token",
  "asn": $asn,
  "limit": $limit,
  "curlWorkers": $workers,
  "masscanCpuPercent": 30,
  "agentId": "fixture-agent"
}
CONFIG
    chmod 0600 "$config_path"
    ;;
  auto-update)
    printf '%s\n' 'fixture auto-update'
    ;;
esac
FIXTURE
  chmod 0755 "$SOURCE_FIXTURE_DIR/cf-san"
fi
[ -x "$SOURCE_FIXTURE_DIR/cf-san" ] || fail "missing Linux cf-san fixture: $SOURCE_FIXTURE_DIR/cf-san"
cp "$SOURCE_FIXTURE_DIR/cf-san" "$FIXTURE_DIR/"

PACKAGE_LOG="$TEMP_DIR/package-manager.log"
CURL_LOG="$TEMP_DIR/curl.log"
SERVICE_LOG="$TEMP_DIR/service.log"
SLEEP_LOG="$TEMP_DIR/sleep.log"
OD_LOG="$TEMP_DIR/od.log"
export PACKAGE_LOG CURL_LOG SERVICE_LOG SLEEP_LOG OD_LOG
export CF_SAN_FIXTURE_DIR="$FIXTURE_DIR" STUB_DIR

create_masscan_stub() {
  cat > "$STUB_DIR/masscan" <<'STUB'
#!/bin/sh
exit 0
STUB
  chmod 0755 "$STUB_DIR/masscan"
}

create_cpulimit_stub() {
  cat > "$STUB_DIR/cpulimit" <<'STUB'
#!/bin/sh
exit 0
STUB
  chmod 0755 "$STUB_DIR/cpulimit"
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
  chmod 0755 "$STUB_DIR/masscan"
  ;;
esac
case "$*" in *cpulimit*)
  cat > "$STUB_DIR/cpulimit" <<'CPULIMIT'
#!/bin/sh
exit 0
CPULIMIT
  chmod 0755 "$STUB_DIR/cpulimit"
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
  chmod 0755 "$STUB_DIR/masscan"
  ;;
esac
case "$*" in *cpulimit*)
  cat > "$STUB_DIR/cpulimit" <<'CPULIMIT'
#!/bin/sh
exit 0
CPULIMIT
  chmod 0755 "$STUB_DIR/cpulimit"
  ;;
esac
STUB
cat > "$STUB_DIR/dpkg-query" <<'STUB'
#!/bin/sh
exit 1
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
output=
last=
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
cat > "$STUB_DIR/update-rc.d" <<'STUB'
#!/bin/sh
set -eu
printf 'update-rc.d %s\n' "$*" >> "$SERVICE_LOG"
if [ "${MOCK_UPDATE_RC_FAILURE:-0}" = 1 ]; then
  exit 1
fi
STUB
cat > "$STUB_DIR/service" <<'STUB'
#!/bin/sh
set -eu
printf 'service %s\n' "$*" >> "$SERVICE_LOG"
if [ "${MOCK_SERVICE_START_FAILURE:-0}" = 1 ]; then
  exit 1
fi
STUB
cat > "$STUB_DIR/rc-update" <<'STUB'
#!/bin/sh
set -eu
printf 'rc-update %s\n' "$*" >> "$SERVICE_LOG"
if [ "${MOCK_RC_UPDATE_FAILURE:-0}" = 1 ]; then
  exit 1
fi
STUB
cat > "$STUB_DIR/rc-service" <<'STUB'
#!/bin/sh
set -eu
printf 'rc-service %s\n' "$*" >> "$SERVICE_LOG"
if [ "${MOCK_RC_SERVICE_FAILURE:-0}" = 1 ]; then
  exit 1
fi
STUB
cat > "$STUB_DIR/od" <<'STUB'
#!/bin/sh
set -eu
printf '%s\n' "${MOCK_OD_VALUE:-0}" >> "$OD_LOG"
printf ' %s\n' "${MOCK_OD_VALUE:-0}"
STUB
cat > "$STUB_DIR/sleep" <<'STUB'
#!/bin/sh
set -eu
printf '%s\n' "${1:-}" >> "$SLEEP_LOG"
if [ "${MOCK_SLEEP_FAILURE:-0}" = 1 ]; then
  exit 1
fi
STUB
chmod 0755 "$STUB_DIR"/*
export PATH="$STUB_DIR:$PATH"

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

assert_no_credentials() {
  case "$ID" in
    debian) scheduler_job=/etc/cron.d/cf-san-auto-update ;;
    alpine) scheduler_job=/etc/periodic/daily/cf-san-auto-update ;;
  esac
  for scheduler_file in \
    /usr/local/libexec/cf-san-auto-update \
    "$scheduler_job" \
    /var/log/cf-san/update.log; do
    [ -f "$scheduler_file" ] || fail "missing scheduler file: $scheduler_file"
    if grep -Fq 'installer-test-secret' "$scheduler_file" || \
      grep -Fq 'updated-token' "$scheduler_file" || \
      grep -Fq 'https://worker.example' "$scheduler_file" || \
      grep -Fq 'https://updated.example' "$scheduler_file" || \
      grep -Fq 'https://custom.example' "$scheduler_file" || \
      grep -Fq 'https://legacy.example' "$scheduler_file"; then
      fail "scheduler file contains credentials or backend URL: $scheduler_file"
    fi
  done
}

assert_scheduler_files() {
  test "$(stat -c '%a' /usr/local/libexec/cf-san-auto-update)" = 750 || fail 'scheduler wrapper mode is not 0750'
  test "$(stat -c '%a' /var/log/cf-san/update.log)" = 600 || fail 'update log mode is not 0600'
  test "$(stat -c '%a' /var/log/cf-san)" = 700 || fail 'update log directory is not root-only'
  case "$ID" in
    debian)
      CRON_FILE=/etc/cron.d/cf-san-auto-update
      [ -f "$CRON_FILE" ] || fail 'Debian cron job was not installed'
      test "$(grep -c 'cf-san-auto-update' "$CRON_FILE")" -eq 1 || fail 'Debian cron job was duplicated'
      test "$(stat -c '%a' "$CRON_FILE")" = 644 || fail 'Debian cron job mode is not 0644'
      grep -Eq '^17 3 \* \* \* root /usr/local/libexec/cf-san-auto-update$' "$CRON_FILE" || fail 'Debian cron timing or command is incorrect'
      ;;
    alpine)
      PERIODIC_FILE=/etc/periodic/daily/cf-san-auto-update
      [ -x "$PERIODIC_FILE" ] || fail 'Alpine daily job was not installed executable'
      test "$(grep -c 'cf-san-auto-update' "$PERIODIC_FILE")" -eq 1 || fail 'Alpine daily job was duplicated'
      test "$(stat -c '%a' "$PERIODIC_FILE")" = 700 || fail 'Alpine daily job mode is not 0700'
      grep -Fxq '#!/bin/sh' "$PERIODIC_FILE" || fail 'Alpine daily job is not a shell script'
      ;;
  esac
  assert_no_credentials
}

assert_activation() {
  case "$ID" in
    debian)
      grep -q '^update-rc.d cron defaults$' "$SERVICE_LOG" || \
        grep -q '^systemctl enable --now cron$' "$SERVICE_LOG" || fail 'Debian cron was not activated'
      grep -q '^service cron start$' "$SERVICE_LOG" || \
        grep -q '^systemctl enable --now cron$' "$SERVICE_LOG" || fail 'Debian cron was not started'
      ;;
    alpine)
      grep -q '^rc-update add crond default$' "$SERVICE_LOG" || fail 'Alpine crond was not enabled'
      grep -q '^rc-service crond start$' "$SERVICE_LOG" || fail 'Alpine crond was not started'
      ;;
  esac
}

assert_update_packages() {
  case "$ID" in
    debian)
      grep -q '^apt-get update$' "$PACKAGE_LOG"
      grep -q 'apt-get install -y masscan' "$PACKAGE_LOG"
      grep -q "apt-get install -y .*${MOCK_PCAP_PACKAGE}" "$PACKAGE_LOG"
      grep -q 'apt-get install -y .*cpulimit' "$PACKAGE_LOG"
      grep -q 'apt-get install -y cron' "$PACKAGE_LOG"
      ;;
    alpine)
      grep -q '^apk add --no-cache masscan cpulimit libpcap-dev$' "$PACKAGE_LOG"
      ;;
  esac
}

for arch_case in 'x86_64 amd64' 'aarch64 arm64'; do
  set -- $arch_case
  mock_arch=$1
  release_arch=$2
  export MOCK_ARCH=$mock_arch
  archive="$FIXTURE_DIR/cf-san_linux_$release_arch.tar.gz"
  checksum="$archive.sha256"
  tar -czf "$archive" -C "$FIXTURE_DIR" cf-san
  sha256sum "$archive" > "$checksum"
  : > "$PACKAGE_LOG"
  : > "$CURL_LOG"
  : > "$SERVICE_LOG"
  : > "$SLEEP_LOG"
  : > "$OD_LOG"
  export MOCK_UPDATE_RC_FAILURE=0 MOCK_SERVICE_START_FAILURE=0
  export MOCK_RC_UPDATE_FAILURE=0 MOCK_RC_SERVICE_FAILURE=0
  export MOCK_SLEEP_FAILURE=0 MOCK_OD_VALUE=0
  rm -f "$STUB_DIR/masscan" "$STUB_DIR/cpulimit"

  install -d /usr/local/bin /usr/local/share/cf-san
  printf '#!/bin/sh\nexit 0\n' > /usr/local/bin/zgrab2
  chmod 0755 /usr/local/bin/zgrab2
  printf 'old bundle\n' > /usr/local/share/cf-san/zgrab2-blocklist.conf

  install_output=$(CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" \
    --url 'https://worker.example' --token 'installer-test-secret' 2>&1) || {
      printf '%s\n' "$install_output" >&2
      exit 1
    }
  case "$install_output" in *installer-test-secret*) fail 'installer echoed token' ;; esac
  [ -x /usr/local/bin/cf-san ] || fail 'cf-san was not installed'
  command -v masscan >/dev/null 2>&1 || fail 'masscan was not installed'
  command -v cpulimit >/dev/null 2>&1 || fail 'cpulimit was not installed'
  [ ! -e /usr/local/bin/zgrab2 ] || fail 'legacy zgrab2 binary was not removed'
  [ ! -e /usr/local/share/cf-san/zgrab2-blocklist.conf ] || fail 'legacy bundled blocklist was not removed'
  test "$(stat -c '%a' /etc/cf-san/config.json)" = 600 || fail 'config permissions are not 0600'
  grep -q 'installer-test-secret' /etc/cf-san/config.json || fail 'agent config omitted token'
  grep -q "cf-san_linux_$release_arch.tar.gz" "$CURL_LOG" || fail "installer selected the wrong release architecture ($release_arch)"
  case "$ID" in
    debian)
      grep -q '^apt-get update$' "$PACKAGE_LOG"
      grep -q 'apt-get install -y .*masscan' "$PACKAGE_LOG"
      grep -q "apt-get install -y .*${MOCK_PCAP_PACKAGE}" "$PACKAGE_LOG"
      grep -q 'apt-get install -y .*cpulimit' "$PACKAGE_LOG"
      grep -q 'apt-get install -y .*cron' "$PACKAGE_LOG"
      ;;
    alpine)
      grep -q '^apk add --no-cache .*masscan' "$PACKAGE_LOG"
      grep -q '^apk add --no-cache .*cpulimit' "$PACKAGE_LOG"
      grep -q '^apk add --no-cache .*libpcap-dev' "$PACKAGE_LOG" || fail 'Alpine install did not include the libpcap.so linker name required by Masscan'
      ;;
    *) fail "unexpected container OS: ${ID:-unset}" ;;
  esac
  if grep -q 'zmap' "$PACKAGE_LOG"; then fail 'installer still installs ZMap'; fi
  assert_scheduler_files
  assert_activation

  case "$ID" in
    debian) scheduler_job=/etc/cron.d/cf-san-auto-update ;;
    alpine) scheduler_job=/etc/periodic/daily/cf-san-auto-update ;;
  esac
  rm -f "$scheduler_job" /usr/local/libexec/cf-san-auto-update
  : > "$SERVICE_LOG"
  repaired_scheduler_output=$(CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
    CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only 2>&1) || {
      printf '%s\n' "$repaired_scheduler_output" >&2
      exit 1
    }
  case "$repaired_scheduler_output" in *updated-token*) fail 'scheduler repair echoed the API token' ;; esac
  assert_scheduler_files
  assert_activation
  test "$(stat -c '%a' /etc/cf-san/config.json)" = 600 || fail 'config permissions changed during scheduler repair'
  REPAIRED_WRAPPER="$TEMP_DIR/repaired-wrapper-$release_arch"
  REPAIRED_JOB="$TEMP_DIR/repaired-job-$release_arch"
  cp /usr/local/libexec/cf-san-auto-update "$REPAIRED_WRAPPER"
  cp "$scheduler_job" "$REPAIRED_JOB"
  : > "$SERVICE_LOG"
  idempotent_scheduler_output=$(CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
    CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only 2>&1) || {
      printf '%s\n' "$idempotent_scheduler_output" >&2
      exit 1
    }
  case "$idempotent_scheduler_output" in *updated-token*) fail 'idempotent scheduler repair echoed the API token' ;; esac
  cmp -s "$REPAIRED_WRAPPER" /usr/local/libexec/cf-san-auto-update || fail 'idempotent update changed the scheduler wrapper'
  cmp -s "$REPAIRED_JOB" "$scheduler_job" || fail 'idempotent update changed the scheduler job'
  assert_scheduler_files
  assert_activation
  test "$(stat -c '%a' /etc/cf-san/config.json)" = 600 || fail 'config permissions changed during idempotent update'

  /usr/local/bin/cf-san configure --url 'https://worker.example' --token 'old-token' --asn 16509 --limit 250 --workers 40
  DEFAULT_CONFIG_BEFORE="$TEMP_DIR/default-config-$release_arch.json"
  cp /etc/cf-san/config.json "$DEFAULT_CONFIG_BEFORE"
  CUSTOM_CONFIG_PATH="$TEMP_DIR/custom-$release_arch/config.json"
  CF_SAN_CONFIG_PATH="$CUSTOM_CONFIG_PATH" /usr/local/bin/cf-san configure \
    --url 'https://custom.example' --token 'custom-token' --asn 64512 --limit 175 --workers 24
  CUSTOM_CONFIG_BEFORE="$TEMP_DIR/custom-config-$release_arch.json"
  cp "$CUSTOM_CONFIG_PATH" "$CUSTOM_CONFIG_BEFORE"
  : > "$PACKAGE_LOG"
  : > "$CURL_LOG"
  : > "$SERVICE_LOG"
  rm -f "$STUB_DIR/masscan" "$STUB_DIR/cpulimit"
  update_output=$(CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
    CF_SAN_CONFIG_PATH="$CUSTOM_CONFIG_PATH" CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only 2>&1) || {
      printf '%s\n' "$update_output" >&2
      exit 1
    }
  case "$update_output" in *updated-token*) fail 'update installer echoed the API token' ;; esac
  command -v masscan >/dev/null 2>&1 || fail 'update-only did not install missing masscan'
  command -v cpulimit >/dev/null 2>&1 || fail 'update-only did not install missing cpulimit'
  assert_update_packages
  assert_scheduler_files
  assert_activation
  cmp -s "$DEFAULT_CONFIG_BEFORE" /etc/cf-san/config.json || fail 'update-only changed the default config'
  cmp -s "$CUSTOM_CONFIG_BEFORE" "$CUSTOM_CONFIG_PATH" || fail 'update-only changed the custom config'
  test "$(stat -c '%a' /etc/cf-san/config.json)" = 600 || fail 'update-only changed the default config mode'
  test "$(stat -c '%a' "$CUSTOM_CONFIG_PATH")" = 600 || fail 'update-only changed the custom config mode'

  : > "$CURL_LOG"
  pinned_output=$(CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
    CF_SAN_CONFIG_PATH="$CUSTOM_CONFIG_PATH" CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" \
    --update-only --version v1.2.3 2>&1) || {
      printf '%s\n' "$pinned_output" >&2
      exit 1
    }
  case "$pinned_output" in *updated-token*) fail 'pinned update echoed the API token' ;; esac
  grep -q "/releases/download/v1.2.3/cf-san_linux_$release_arch.tar.gz" "$CURL_LOG" || fail 'pinned update did not select the exact release URL'
  if grep -q '/releases/latest/download' "$CURL_LOG"; then fail 'pinned update used the latest-release URL'; fi
  assert_scheduler_files
  cmp -s "$CUSTOM_CONFIG_BEFORE" "$CUSTOM_CONFIG_PATH" || fail 'pinned update changed the custom config'
  test "$(stat -c '%a' "$CUSTOM_CONFIG_PATH")" = 600 || fail 'pinned update changed the custom config mode'

  CHECKSUM_BINARY_BEFORE="$TEMP_DIR/checksum-binary-$release_arch"
  cp /usr/local/bin/cf-san "$CHECKSUM_BINARY_BEFORE"
  printf '%064d\n' 0 > "$checksum"
  : > "$CURL_LOG"
  if CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
    CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only >"$TEMP_DIR/checksum-mismatch-$release_arch.out" 2>&1; then
    fail 'checksum mismatch unexpectedly succeeded'
  fi
  grep -q 'release checksum does not match' "$TEMP_DIR/checksum-mismatch-$release_arch.out" || fail 'checksum mismatch failure was not actionable'
  cmp -s "$CHECKSUM_BINARY_BEFORE" /usr/local/bin/cf-san || fail 'checksum mismatch replaced the cf-san binary'
  sha256sum "$archive" > "$checksum"

  : > "$SLEEP_LOG"
  : > /var/log/cf-san/update.log
  for expected_delay in 0 1800; do
    export MOCK_OD_VALUE=$expected_delay
    /usr/local/libexec/cf-san-auto-update
    test "$(tail -n 1 "$SLEEP_LOG")" = "$expected_delay" || fail "scheduler delay was not $expected_delay seconds"
  done
  grep -q 'fixture auto-update' /var/log/cf-san/update.log || fail 'scheduler wrapper did not invoke cf-san auto-update'
  assert_no_credentials

  # Update-only must leave legacy config migration to the replacement agent;
  # it must not invoke configure and overwrite the file with installer defaults.
  LEGACY_CONFIG_PATH="$TEMP_DIR/legacy-$release_arch/config.json"
  mkdir -p "$(dirname "$LEGACY_CONFIG_PATH")"
  printf '%s\n' '{"url":"https://legacy.example","token":"legacy-token","asn":16509,"limit":250,"senders":8192,"agentId":"legacy-agent"}' > "$LEGACY_CONFIG_PATH"
  LEGACY_CONFIG_BEFORE="$TEMP_DIR/legacy-config-$release_arch.json"
  cp "$LEGACY_CONFIG_PATH" "$LEGACY_CONFIG_BEFORE"
  legacy_update_output=$(CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
    CF_SAN_CONFIG_PATH="$LEGACY_CONFIG_PATH" CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only 2>&1) || {
      printf '%s\n' "$legacy_update_output" >&2
      exit 1
    }
  case "$legacy_update_output" in *'curl worker count cannot exceed'*) fail 'update rejected an over-limit legacy sender count' ;; esac
  cmp -s "$LEGACY_CONFIG_BEFORE" "$LEGACY_CONFIG_PATH" || fail 'update-only changed the legacy config'
  assert_scheduler_files

  # Existing Masscan installations may still be missing libpcap on Debian
  # Bookworm. Updating cf-san must repair that dependency even when the binary
  # is already present.
  : > "$PACKAGE_LOG"
  : > "$SERVICE_LOG"
  create_masscan_stub
  create_cpulimit_stub
  existing_update_output=$(CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
    CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only 2>&1) || {
      printf '%s\n' "$existing_update_output" >&2
      exit 1
    }
  case "$ID" in
    debian)
      grep -q "apt-get install -y ${MOCK_PCAP_PACKAGE}" "$PACKAGE_LOG" || fail 'update did not repair the missing Masscan libpcap dependency'
      if grep -q 'apt-get install -y masscan' "$PACKAGE_LOG"; then fail 'update reinstalled an existing Masscan binary unnecessarily'; fi
      ;;
    alpine)
      grep -q '^apk add --no-cache libpcap-dev$' "$PACKAGE_LOG" || fail 'Alpine update did not repair the Masscan libpcap.so linker name'
      if grep -q 'apk add --no-cache masscan' "$PACKAGE_LOG"; then fail 'update reinstalled an existing Masscan binary unnecessarily'; fi
      ;;
  esac
  assert_scheduler_files
  printf 'installer %s/%s install, update, pinning, wrapper, and idempotency checks passed\n' "$ID" "$release_arch"

  rm -f /usr/local/bin/cf-san /usr/local/bin/zgrab2 /etc/cf-san/config.json
  rm -rf /etc/cf-san /usr/local/share/cf-san /var/log/cf-san
  rm -f /usr/local/libexec/cf-san-auto-update /etc/cron.d/cf-san-auto-update /etc/periodic/daily/cf-san-auto-update
done

# A failed scheduler activation must be actionable and must stop before any
# release download or binary replacement.
create_masscan_stub
create_cpulimit_stub
: > "$CURL_LOG"
: > "$SERVICE_LOG"
case "$ID" in
  debian)
    export MOCK_SERVICE_START_FAILURE=1 MOCK_UPDATE_RC_FAILURE=0
    ;;
  alpine)
    export MOCK_RC_SERVICE_FAILURE=1 MOCK_RC_UPDATE_FAILURE=0
    ;;
esac
if CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
  CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only >"$TEMP_DIR/service-failure.out" 2>&1; then
  fail 'installer accepted a scheduler service activation failure'
fi
case "$ID" in
  debian) grep -q 'cannot enable the Debian cron service' "$TEMP_DIR/service-failure.out" || fail 'Debian service failure was not actionable' ;;
  alpine) grep -q 'cannot enable the Alpine crond service' "$TEMP_DIR/service-failure.out" || fail 'Alpine service failure was not actionable' ;;
esac
[ ! -s "$CURL_LOG" ] || fail 'scheduler activation failure downloaded a release'
export MOCK_SERVICE_START_FAILURE=0 MOCK_UPDATE_RC_FAILURE=0
export MOCK_RC_SERVICE_FAILURE=0 MOCK_RC_UPDATE_FAILURE=0

if [ "$ID" = debian ]; then
  cat > "$STUB_DIR/systemctl" <<'STUB'
#!/bin/sh
set -eu
printf 'systemctl %s\n' "$*" >> "$SERVICE_LOG"
if [ "${MOCK_SYSTEMCTL_FAILURE:-0}" = 1 ]; then
  exit 1
fi
STUB
  chmod 0755 "$STUB_DIR/systemctl"
  export MOCK_SYSTEMCTL_FAILURE=1
  if CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
    CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only >"$TEMP_DIR/systemctl-failure.out" 2>&1; then
    fail 'installer accepted a systemctl activation failure'
  fi
  grep -q 'systemctl enable --now cron failed' "$TEMP_DIR/systemctl-failure.out" || fail 'systemctl failure was not actionable'
  export MOCK_SYSTEMCTL_FAILURE=0
  : > "$SERVICE_LOG"
  CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
    CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only >/dev/null 2>&1 || fail 'systemctl activation path failed'
  grep -q '^systemctl enable --now cron$' "$SERVICE_LOG" || fail 'installer did not prefer systemctl when available'
  rm -f "$STUB_DIR/systemctl"
fi

# Task 5 safety ruling: even with Masscan and cpulimit already present, an
# unsupported ID must fail before release download or binary replacement.
create_masscan_stub
create_cpulimit_stub
OS_RELEASE_BACKUP="$TEMP_DIR/os-release.backup"
cp /etc/os-release "$OS_RELEASE_BACKUP"
printf '%s\n' 'ID=unsupported-test-distro' 'NAME="Unsupported Test Distro"' > /etc/os-release
printf '#!/bin/sh\nprintf old-binary\n' > /usr/local/bin/cf-san
chmod 0755 /usr/local/bin/cf-san
cp /usr/local/bin/cf-san "$TEMP_DIR/cf-san.before"
: > "$CURL_LOG"
if CF_SAN_BACKEND_URL='https://updated.example' CF_SAN_API_TOKEN='updated-token' \
  CF_SAN_RELEASE_REPOSITORY='example/cf-san' sh "$INSTALLER" --update-only >"$TEMP_DIR/unsupported.out" 2>&1; then
  cp "$OS_RELEASE_BACKUP" /etc/os-release
  fail 'installer accepted an unsupported distribution in update-only mode'
fi
grep -q 'unsupported Linux distribution' "$TEMP_DIR/unsupported.out" || {
  cp "$OS_RELEASE_BACKUP" /etc/os-release
  fail 'unsupported distribution failure was not actionable'
}
[ ! -s "$CURL_LOG" ] || {
  cp "$OS_RELEASE_BACKUP" /etc/os-release
  fail 'unsupported distribution downloaded a release'
}
cmp -s "$TEMP_DIR/cf-san.before" /usr/local/bin/cf-san || {
  cp "$OS_RELEASE_BACKUP" /etc/os-release
  fail 'unsupported distribution replaced the cf-san binary'
}
cp "$OS_RELEASE_BACKUP" /etc/os-release

printf 'installer mocked integration checks passed for %s\n' "$ID"
