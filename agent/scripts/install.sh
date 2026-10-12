#!/bin/sh
set -eu

DEFAULT_RELEASE_REPOSITORY='__CF_SAN_RELEASE_REPOSITORY__'
BACKEND_URL=${CF_SAN_BACKEND_URL:-}
API_TOKEN=${CF_SAN_API_TOKEN:-}
UPDATE_ONLY=0
UPDATE_VERSION=
DISTRO_ID=
CRON_PACKAGE_READY=0

fail() {
  printf 'cf-san installer: %s\n' "$1" >&2
  exit 1
}

usage() {
  printf 'Usage: install.sh --url https://worker.example --token TOKEN [--update-only [--version vMAJOR.MINOR.PATCH]]\n' >&2
}

is_stable_version() {
  stable_version=$1
  case "$stable_version" in
    *[[:cntrl:]]*) return 1 ;;
  esac
  printf '%s\n' "$stable_version" |
    LC_ALL=C grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
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
    --update-only)
      UPDATE_ONLY=1
      shift
      ;;
    --version)
      [ "$#" -ge 2 ] || { usage; fail 'missing value for --version'; }
      [ -n "$2" ] || { usage; fail 'missing value for --version'; }
      UPDATE_VERSION=$2
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

if [ -n "$UPDATE_VERSION" ]; then
  [ "$UPDATE_ONLY" -eq 1 ] || fail '--version requires --update-only'
  is_stable_version "$UPDATE_VERSION" || \
    fail 'version must be a stable vMAJOR.MINOR.PATCH tag'
fi

[ -n "$BACKEND_URL" ] || { usage; fail 'backend URL is required'; }
case "$BACKEND_URL" in https://*) ;; *) fail 'backend URL must use HTTPS' ;; esac
[ -n "$API_TOKEN" ] || { usage; fail 'API token is required'; }

RELEASE_REPOSITORY=${CF_SAN_RELEASE_REPOSITORY:-$DEFAULT_RELEASE_REPOSITORY}
printf '%s\n' "$RELEASE_REPOSITORY" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || \
  fail 'release repository is not configured on the Worker'

[ "$(id -u)" -eq 0 ] || fail 'run this script with sudo or as root'

detect_supported_os() {
  [ -r /etc/os-release ] || fail 'cannot identify Linux distribution'
  # shellcheck disable=SC1091
  . /etc/os-release
  DISTRO_ID=${ID:-}
  case "$DISTRO_ID" in
    debian|alpine) ;;
    *) fail "unsupported Linux distribution: ${DISTRO_ID:-unknown}; supported: Debian and Alpine" ;;
  esac
}

detect_supported_os

debian_pcap_package() {
  if apt-cache show libpcap0.8t64 2>/dev/null | grep -q '^Package: libpcap0.8t64$'; then
    printf '%s\n' libpcap0.8t64
  else
    printf '%s\n' libpcap0.8
  fi
}

ensure_debian_pcap() {
  command -v apt-get >/dev/null 2>&1 || fail 'apt-get is required on Debian'
  command -v apt-cache >/dev/null 2>&1 || fail 'apt-cache is required on Debian'
  command -v dpkg-query >/dev/null 2>&1 || fail 'dpkg-query is required on Debian'
  if dpkg-query -s libpcap0.8t64 >/dev/null 2>&1 || dpkg-query -s libpcap0.8 >/dev/null 2>&1; then
    return 0
  fi
  apt-get update
  PCAP_PACKAGE=$(debian_pcap_package)
  DEBIAN_FRONTEND=noninteractive apt-get install -y "$PCAP_PACKAGE"
}

ensure_alpine_pcap() {
  command -v apk >/dev/null 2>&1 || fail 'apk is required on Alpine'
  # Masscan 1.3.2 loads the unversioned libpcap.so name at runtime. Alpine's
  # libpcap package only provides libpcap.so.1; the -dev package adds the link.
  apk add --no-cache libpcap-dev
}

ensure_debian_cron_package() {
  command -v apt-get >/dev/null 2>&1 || fail 'apt-get is required on Debian'
  if ! apt-get update; then
    fail 'cannot refresh Debian package indexes while installing cron'
  fi
  if ! DEBIAN_FRONTEND=noninteractive apt-get install -y cron; then
    fail 'cannot install the Debian cron package'
  fi
  CRON_PACKAGE_READY=1
}

write_scheduler_files() {
  command -v od >/dev/null 2>&1 || fail 'od is required to randomize the daily update delay'
  command -v sleep >/dev/null 2>&1 || fail 'sleep is required to randomize the daily update delay'
  command -v tr >/dev/null 2>&1 || fail 'tr is required to randomize the daily update delay'

  if ! install -d -m 0700 /var/log/cf-san; then
    fail 'cannot create the root-only cf-san update log directory'
  fi
  if [ ! -e /var/log/cf-san/update.log ]; then
    if ! (umask 077 && : > /var/log/cf-san/update.log); then
      fail 'cannot create the cf-san update log'
    fi
  fi
  if ! chmod 0600 /var/log/cf-san/update.log; then
    fail 'cannot protect the cf-san update log'
  fi

  if ! install -d -m 0755 /usr/local/libexec; then
    fail 'cannot create the cf-san scheduler directory'
  fi
  WRAPPER_SOURCE="$TEMP_DIR/cf-san-auto-update"
  cat > "$WRAPPER_SOURCE" <<'WRAPPER'
#!/bin/sh
set -eu

exec >> /var/log/cf-san/update.log 2>&1

raw=$(od -An -N4 -tu4 /dev/urandom | tr -d '[:space:]')
case "$raw" in
  ''|*[!0-9]*)
    printf '%s\n' 'cf-san auto-update: cannot generate a random delay' >&2
    exit 1
    ;;
esac
delay=$((raw % 1801))
sleep "$delay"
exec /usr/local/bin/cf-san auto-update
WRAPPER
  if ! install -m 0750 "$WRAPPER_SOURCE" /usr/local/libexec/cf-san-auto-update; then
    fail 'cannot install the cf-san scheduler wrapper'
  fi

  case "$DISTRO_ID" in
    debian)
      if ! install -d -m 0755 /etc/cron.d; then
        fail 'cannot create the Debian cron directory'
      fi
      CRON_SOURCE="$TEMP_DIR/cf-san-cron"
      printf '%s\n' '17 3 * * * root /usr/local/libexec/cf-san-auto-update' > "$CRON_SOURCE"
      if ! install -m 0644 "$CRON_SOURCE" /etc/cron.d/cf-san-auto-update; then
        fail 'cannot install the Debian cf-san cron job'
      fi
      ;;
    alpine)
      if ! install -d -m 0755 /etc/periodic/daily; then
        fail 'cannot create the Alpine daily job directory'
      fi
      PERIODIC_SOURCE="$TEMP_DIR/cf-san-periodic"
      printf '%s\n' '#!/bin/sh' 'exec /usr/local/libexec/cf-san-auto-update' > "$PERIODIC_SOURCE"
      if ! install -m 0700 "$PERIODIC_SOURCE" /etc/periodic/daily/cf-san-auto-update; then
        fail 'cannot install the Alpine cf-san daily job'
      fi
      ;;
  esac
}

install_scheduler() {
  case "$DISTRO_ID" in
    debian)
      if [ "$CRON_PACKAGE_READY" -ne 1 ]; then
        ensure_debian_cron_package
      fi
      if command -v systemctl >/dev/null 2>&1; then
        if ! systemctl enable --now cron; then
          fail 'cannot enable the Debian cron service: systemctl enable --now cron failed'
        fi
      elif command -v update-rc.d >/dev/null 2>&1 && command -v service >/dev/null 2>&1; then
        if ! update-rc.d cron defaults; then
          fail 'cannot enable the Debian cron service: update-rc.d cron defaults failed'
        fi
        if ! service cron start; then
          fail 'cannot enable the Debian cron service: service cron start failed'
        fi
      else
        fail 'cannot enable the Debian cron service: systemctl or update-rc.d plus service is required'
      fi
      ;;
    alpine)
      command -v rc-update >/dev/null 2>&1 || fail 'cannot enable the Alpine crond service: rc-update is required'
      command -v rc-service >/dev/null 2>&1 || fail 'cannot enable the Alpine crond service: rc-service is required'
      if ! rc-update add crond default; then
        fail 'cannot enable the Alpine crond service: rc-update add crond default failed'
      fi
      if ! rc-service crond start; then
        fail 'cannot enable the Alpine crond service: rc-service crond start failed'
      fi
      ;;
  esac
  write_scheduler_files
}

install_masscan() {
  case "$DISTRO_ID" in
    debian)
      command -v apt-get >/dev/null 2>&1 || fail 'apt-get is required on Debian'
      command -v apt-cache >/dev/null 2>&1 || fail 'apt-cache is required on Debian'
      apt-get update
      PCAP_PACKAGE=$(debian_pcap_package)
      DEBIAN_FRONTEND=noninteractive apt-get install -y masscan cpulimit "$PCAP_PACKAGE"
      ;;
    alpine)
      command -v apk >/dev/null 2>&1 || fail 'apk is required on Alpine'
      apk add --no-cache masscan cpulimit libpcap-dev
      ;;
    *)
      fail "unsupported Linux distribution: ${DISTRO_ID:-unknown}; supported: Debian and Alpine"
      ;;
  esac
  command -v masscan >/dev/null 2>&1 || fail 'Masscan installation failed; install the masscan package and retry'
}

ensure_cpulimit() {
  if command -v cpulimit >/dev/null 2>&1; then
    return 0
  fi
  case "$DISTRO_ID" in
    debian)
      command -v apt-get >/dev/null 2>&1 || fail 'apt-get is required on Debian'
      apt-get update
      DEBIAN_FRONTEND=noninteractive apt-get install -y cpulimit
      ;;
    alpine)
      command -v apk >/dev/null 2>&1 || fail 'apk is required on Alpine'
      apk add --no-cache cpulimit
      ;;
    *)
      fail "unsupported Linux distribution: ${DISTRO_ID:-unknown}; supported: Debian and Alpine"
      ;;
  esac
  command -v cpulimit >/dev/null 2>&1 || fail 'cpulimit installation failed'
}

if [ "$UPDATE_ONLY" -eq 0 ]; then
  case "$DISTRO_ID" in
    debian)
      command -v apt-get >/dev/null 2>&1 || fail 'apt-get is required on Debian'
      apt-get update
      command -v apt-cache >/dev/null 2>&1 || fail 'apt-cache is required on Debian'
      PCAP_PACKAGE=$(debian_pcap_package)
      DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl coreutils tar gzip masscan cpulimit cron "$PCAP_PACKAGE"
      CRON_PACKAGE_READY=1
      ;;
    alpine)
      command -v apk >/dev/null 2>&1 || fail 'apk is required on Alpine'
      apk add --no-cache ca-certificates curl coreutils tar gzip masscan cpulimit libpcap-dev
      ;;
    *)
      fail "unsupported Linux distribution: ${DISTRO_ID:-unknown}; supported: Debian and Alpine"
      ;;
  esac
else
  for command_name in curl tar gzip sha256sum install awk mktemp; do
    command -v "$command_name" >/dev/null 2>&1 || fail "required update command not found: $command_name"
  done
  if ! command -v masscan >/dev/null 2>&1; then
    install_masscan
  else
    # Masscan 1.3.2 on Debian Bookworm does not declare its dynamically
    # loaded libpcap runtime as a package dependency. Repair older installs too.
    if [ "$DISTRO_ID" = debian ]; then
      command -v apt-get >/dev/null 2>&1 || fail 'apt-get is required on Debian'
      command -v apt-cache >/dev/null 2>&1 || fail 'apt-cache is required on Debian'
      command -v dpkg-query >/dev/null 2>&1 || fail 'dpkg-query is required on Debian'
      ensure_debian_pcap
    elif [ "$DISTRO_ID" = alpine ]; then
      ensure_alpine_pcap
    fi
  fi
fi

ensure_cpulimit
command -v masscan >/dev/null 2>&1 || fail 'Masscan is not installed; install the masscan package and retry'
command -v curl >/dev/null 2>&1 || fail 'curl is not installed; install curl and retry'

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) fail "unsupported CPU architecture: $(uname -m); supported: amd64 and arm64" ;;
esac

TEMP_DIR=$(mktemp -d)
cleanup() { rm -rf -- "$TEMP_DIR"; }
trap cleanup 0
install_scheduler
ARCHIVE="cf-san_linux_${ARCH}.tar.gz"
if [ -n "$UPDATE_VERSION" ]; then
  BASE_URL="https://github.com/${RELEASE_REPOSITORY}/releases/download/${UPDATE_VERSION}"
else
  BASE_URL="https://github.com/${RELEASE_REPOSITORY}/releases/latest/download"
fi
curl --fail --silent --show-error --location --retry 3 "${BASE_URL}/${ARCHIVE}" -o "${TEMP_DIR}/${ARCHIVE}"
curl --fail --silent --show-error --location --retry 3 "${BASE_URL}/${ARCHIVE}.sha256" -o "${TEMP_DIR}/${ARCHIVE}.sha256"
EXPECTED_SHA=$(awk 'NR == 1 { print $1 }' "${TEMP_DIR}/${ARCHIVE}.sha256")
printf '%s\n' "$EXPECTED_SHA" | grep -Eq '^[A-Fa-f0-9]{64}$' || fail 'release checksum file is invalid'
ACTUAL_SHA=$(sha256sum "${TEMP_DIR}/${ARCHIVE}" | awk '{ print $1 }')
[ "$ACTUAL_SHA" = "$EXPECTED_SHA" ] || fail 'release checksum does not match'

tar -xzf "${TEMP_DIR}/${ARCHIVE}" -C "$TEMP_DIR"
[ -x "${TEMP_DIR}/cf-san" ] || fail 'release archive does not contain cf-san'
install -m 0755 "${TEMP_DIR}/cf-san" /usr/local/bin/cf-san
install -d -m 0755 /usr/local/share/cf-san
install -d -m 0755 /etc/cf-san

# Remove artifacts that earlier cf-san installers bundled. Do not uninstall a
# system ZMap package because it may be used independently by the host owner.
rm -f /usr/local/bin/zgrab2 \
  /usr/local/share/cf-san/zgrab2-blocklist.conf \
  /usr/local/share/cf-san-THIRD_PARTY_NOTICES.txt

if [ "$UPDATE_ONLY" -eq 1 ]; then
  printf 'cf-san updated. Masscan is installed; ZMap and ZGrab2 are no longer used. Exit and restart `cf-san` to use the new version.\n'
else
  /usr/local/bin/cf-san configure --url "$BACKEND_URL" --token "$API_TOKEN"
  printf 'cf-san installed. Run `cf-san` to initialize settings and open the text menu.\n'
fi
