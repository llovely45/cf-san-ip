# cf-san IP Pool

Cloudflare Workers + D1 IP pool with an authenticated admin UI, `/postip` and `/getip` APIs, and the `cf-san` ASN scanner agent.

## Agent scan scope and behavior

Only run `cf-san` against an ASN and IPv4 prefixes that you own or are explicitly authorized to assess. The agent asks RIPEstat for currently originating IPv4 prefixes, rejects non-public/mixed special-purpose ranges, and scans eligible ranges in numerically ordered batches of up to four blocks, each no larger than `/16` (a source narrower than `/16` remains intact; adjacent source prefixes are never merged). Nested/overlapping source prefixes are scanned only once. Batches are assigned in ascending order; up to four blocks within a batch run concurrently, so completion order within a batch may differ. Masscan may also permute addresses inside a block. The configured packet rate is a global cap shared across the parallel Masscan processes, and the configured curl-worker limit is shared across the batch. The agent invokes Masscan for TCP/443 with a one-second receive wait, then sends only open TCP/443 results to concurrent `curl` requests for `/cdn-cgi/trace`. There is no ZMap or ZGrab2 scan stage. The Worker returns the full current prefix set, the legacy compact cursor, and exact per-block checkpoints; the Go agent lazily generates the block plan instead of materializing it in Worker or D1. Existing `/24` checkpoints are mapped to their containing new block and conservatively rescanned; active blocks are repeated after pause, while completed per-block checkpoints are skipped. If the announced prefix set changes, both cursor and per-block progress reset so newly added lower-address ranges cannot be skipped; the scan range is never capped to make planning faster. Each invocation completes one current prefix plan and then stops; start it again later to refresh RIPEstat.

Multiple machines can scan the same ASN cooperatively. The Worker atomically leases distinct candidate blocks in ascending order; each agent holds at most four blocks concurrently and retries blocks held by other agents. Every ten-minute forced checkpoint report renews its block leases; a lease expires after 30 minutes without a checkpoint, so another agent can reclaim it. Pausing releases owned leases immediately; abrupt exits rely on expiry. Reports from an expired or replaced lease are rejected, preventing a stale process from overwriting newer progress. The PPS cap is per machine, not shared globally: with `N` machines configured for 3000 PPS each, aggregate traffic may approach `N × 3000` PPS.

`--limit` is the aggregate Masscan TCP probe-rate cap in packets per second (default `100`); the agent divides it across the blocks in each batch, so four concurrent blocks at a 3000 PPS setting receive 750 PPS each. It is not a total interface shaper for curl traffic. `--workers` sets the aggregate maximum concurrent curl validations (default `100`), shared across the blocks in each batch. The `--cpu-percent` option (default `30`) limits the aggregate CPU available to all Masscan processes to that percentage of the logical CPUs visible to the agent; menu option 6 can change it from 1 to 100. For example, on a 4-vCPU host a 30% cap is 120 one-core percentage points total; with four active scan blocks, each Masscan is limited to 30%. This is a sampled process throttle, not a kernel/cgroup hard quota, and it applies to Masscan rather than curl or unrelated system processes. The old `--senders` flag is retained as an alias for `--workers`, and saved configs with the old `senders` field are migrated to `curlWorkers`; legacy values above the new 4096-worker ceiling are capped during migration so upgrades remain usable. The agent runs at most four blocks concurrently; if `--limit`, `--workers`, or CPU quota is too low, it automatically reduces block concurrency to keep the configured caps. Masscan's status output stays enabled, and the agent logs each block's worker slot, per-process PPS, and CPU quota so menu option 12 can show the current CIDRs. Set a conservative rate, follow the target network's acceptable-use policy, and pause promptly if asked. The agent never sends the API token to scan targets. The selected public-IP probe uses curl default TLS verification, including the trusted certificate chain and certificate identity matching the target IP; a certificate issued only for a domain name will be rejected. Agent-to-Worker and agent-to-RIPEstat connections also use ordinary TLS verification.

Each open TCP/443 result gets a direct HTTPS GET to `https://<IP>:443/cdn-cgi/trace`, with one-second connect and overall timeouts. A trace response is accepted only when its HTTP headers contain `Server: cloudflare` (case-insensitive), its body does not contain `error code: 1034`, and it contains one usable, non-empty, whitespace-free `loc=` value. HTTP status is not itself a rejection. Missing `loc=` means the result is not saved. Valid candidates are immediately reported to D1; unsuccessful curl responses only update block-level progress. A progress heartbeat is sent every ten minutes and when a block completes, with the latest open TCP/443 IP reported by Masscan when available. Masscan does not emit a reliable ordered cursor for every attempted/non-responsive address, so the heartbeat is not an exact last attempted IP.

The trace URL uses an IP literal, not a per-target business hostname. If an Nginx stream proxy routes only when a domain SNI is present, this IP-only probe will not select that domain-specific route; testing such a route requires a known hostname/SNI.

## Worker configuration for Agent installation

The authenticated UI's **安装 cf-san Agent** panel creates a command with `--url` and `--token`. It warns that the token may remain in shell history or be visible in local process arguments. The installer URL itself has no token query parameter, and the Worker does not log the token.

The non-secret Worker variable `CF_SAN_RELEASE_REPOSITORY` is configured in `wrangler.jsonc` as `llovely45/cf-san-ip`. `/install` returns a clear `503` if this variable is missing or malformed. Do not put the API token in this variable. Keep it aligned with the repository where the `v*` release workflow publishes assets.

The tag workflow runs the full Worker, UI, Go, migration, and installer checks. For a `v*` tag it creates:

- `cf-san_linux_amd64.tar.gz` and its `.sha256` file
- `cf-san_linux_arm64.tar.gz` and its `.sha256` file

Each archive contains only the Go `cf-san` agent. The installer installs Masscan, `cpulimit`, and their runtime dependencies through the Debian or Alpine package manager, then verifies the agent archive SHA-256. On Debian it explicitly installs or repairs Masscan's `libpcap` runtime, including the Bookworm/Trixie package-name difference. On Alpine it installs `libpcap-dev`: Masscan 1.3.2 loads the unversioned `libpcap.so` name, which is not provided by Alpine's runtime-only `libpcap` package. Both install and update paths ensure the correct package. The old bundled `zgrab2` binary and shared blocklist are removed when upgrading. Existing system-installed ZMap packages are not automatically purged, but the new agent no longer invokes or requires ZMap.

## Linux installation and menu

The generated command is intended for a Debian or Alpine Linux host with outbound HTTPS access and root/sudo privileges. It requires `curl` and a POSIX `sh` to fetch and run the script; the script installs Masscan, `cpulimit`, CA certificates, and archive utilities, then installs `cf-san` under `/usr/local/bin`.

Run `sudo cf-san` to open the text menu. It supports:

1. Initialize/check Masscan, `cpulimit`, and curl availability, then check backend authentication, ASN, and the current scan plan.
2. Start a one-pass scan in the background.
3. Pause the scan with SIGTERM; the active block remains incomplete.
4. Set Masscan rate in packets per second.
5. Set concurrent curl validation workers.
6. Set the aggregate Masscan CPU cap, as a percentage of the host's visible logical CPU capacity (default 30%, range 1–100%).
7. Set ASN.
8. View the backend address, plaintext API token, ASN, PPS rate, CPU cap, curl concurrency, Agent ID, and process status.
9. Change the backend HTTPS address.
10. Change the API token.
11. After pausing any active scan, fetch the current installer script from the configured Worker and install the latest GitHub Release of `cf-san`. If Masscan or `cpulimit` is missing, this update path installs it; it also repairs the required `libpcap` package on Debian and Alpine (including Alpine's unversioned linker name).
12. Display the latest 100 lines from `/var/log/cf-san/agent.log`; use the shown `tail -f` command for live following.
13. Exit. After updating, exit and run `sudo cf-san` again to load the new binary.

## Daily automatic updates

The installer configures one root-owned daily update job on Debian or Alpine. It installs the credential-free wrapper `/usr/local/libexec/cf-san-auto-update` with mode `0750`. The wrapper reads a random delay from `/dev/urandom`, sleeps for `0`–`1800` seconds (0–30 minutes), and then runs `/usr/local/bin/cf-san auto-update`. Its standard output and error are appended to `/var/log/cf-san/update.log`; neither scheduler file nor the wrapper contains the backend URL or API token.

- On Debian, the installer installs the `cron` package, writes `/etc/cron.d/cf-san-auto-update` with a root job at `03:17` local time, and enables/starts `cron` with `systemctl enable --now cron`. On hosts without `systemctl`, it uses `update-rc.d cron defaults` and `service cron start`.
- On Alpine, the installer writes executable `/etc/periodic/daily/cf-san-auto-update` and enables/starts `crond` with `rc-update add crond default` and `rc-service crond start`.

The installer requires a supported Debian or Alpine system, root/sudo privileges, outbound HTTPS, and a POSIX `sh`; it installs or repairs the required `curl`, Masscan, `cpulimit`, CA certificates, archive tools, and the Debian/Alpine `libpcap` dependency. Initial installation and `--update-only` scheduler setup are idempotent. An unsupported OS or failed package/service activation stops with an error instead of silently disabling updates.

Each automatic run takes `/run/lock/cf-san-update.lock`, shared with the menu's manual update, pause, start, and configuration operations. It loads the existing `0600` configuration, checks the embedded stable version and release repository, and queries the latest stable GitHub release. Equal or older releases, invalid build identity, and release-check failures do not stop a scan. Only a strictly newer release starts the update lifecycle.

When a newer release is available, an active scan receives `SIGTERM` and the updater waits up to two minutes, polling once per second, until the validated scanner process has stopped. It never replaces the executable while that process is alive. The installer then fetches the exact checked tag and verifies its SHA-256 archive before replacement. A scan that was stopped before the check remains stopped after a successful update; a scan that was active before the update is started again only after a successful installation. If installation fails, the updater attempts to resume only that formerly active scan. A shutdown timeout or other shutdown failure prevents installation. The interrupted block remains incomplete and is eligible for a later resume; normal pause releases its leases immediately.

Update outcomes are recorded in `/var/log/cf-san/update.log`, in a mode `0600` file inside the root-only `/var/log/cf-san` directory. The log records the current/latest versions, prior scan state, result, and sanitized errors. At 1 MiB it rotates the current file and retains three numbered backups: `update.log.1`, `.2`, and `.3`. The scanner's separate `/var/log/cf-san/agent.log` is unchanged. A failed release lookup, download, checksum, installation, or restart returns a nonzero result and is retried by the next daily job; retry the check manually with `sudo cf-san auto-update --config /etc/cf-san/config.json`. Archive and checksum downloads themselves use up to three curl retries, and a checksum failure aborts before replacing the installed binary.

Manual controls remain explicit: menu option 3 sends the scanner `SIGTERM` as a pause/stop signal, leaving the active block incomplete for a later run; menu option 11 requires the scanner to be stopped before a manual latest-release update and uses the same update lock. To disable only this automatic job, remove its job file and leave the host's shared scheduler daemon running:

```sh
# Debian
sudo rm -f /etc/cron.d/cf-san-auto-update

# Alpine
sudo rm -f /etc/periodic/daily/cf-san-auto-update
```

Removing the job file is sufficient; do not stop the host's shared cron or `crond` daemon, because other scheduled jobs may use it.

Local settings are stored at `/etc/cf-san/config.json`, owner-readable only (`0600`); credentials remain plaintext by design, so protect the host and backups. Runtime PID metadata is under `/run/cf-san`; logs are under `/var/log/cf-san/agent.log`. The first version does not install a boot-time service.

CLI settings override saved values, for example:

```sh
sudo cf-san configure --url 'https://worker.example' --token 'YOUR_API_TOKEN' --asn 906 --limit 100 --workers 100 --cpu-percent 30
sudo cf-san run --config /etc/cf-san/config.json
```

`run` executes one current plan in the foreground. The menu starts it in the background and provides the pause control.

## Agent API

Both endpoints require `Authorization: Bearer <API_TOKEN>`:

- `GET /api/agent/v1/plan?asn=906` fetches the live authorized ASN prefix set and returns that complete set, the legacy compact cursor, and per-block checkpoints; it does not materialize the scan plan itself.
- `POST /api/agent/v1/claim` submits up to 64 ordered candidate blocks with an idempotency request ID and acquires up to four exclusive, expiring leases; the Worker enforces the four-lease cap across concurrent requests from the same Agent ID.
- `POST /api/agent/v1/report` sends a lease-bound block heartbeat/completion, optionally with one validated candidate. Each checkpoint renews the lease; candidate persistence and checkpoint update are batched together in D1.
- `POST /api/agent/v1/release` releases a lease when an agent pauses or exits a block early.

These endpoints do not change existing `POST /postip` or `GET /getip` behavior. `scan_checkpoints` remains as the legacy compact cursor; new reports are recorded independently in `scan_block_progress`, keyed by ASN, source prefix, and block CIDR, so four concurrent blocks can resume without one block's progress skipping another. A block is marked completed only after its Masscan scan and curl validations finish. This progress is separate from `ip_records`, which only receives validated Cloudflare IPs, and is not currently shown in the IP-pool table. A changed prefix set clears both cursor and per-block progress; the legacy `scan_blocks` table and its existing history are left intact.

To roll out lease support safely: pause all running agents, apply the migration to the production D1 with `pnpm exec wrangler d1 migrations apply cf-san-ip --remote`, deploy the Worker with `pnpm run deploy`, publish/install the lease-aware Agent release on every machine, then resume scanning. Do not leave older agents running against the new Worker: they cannot claim blocks or submit lease-bound reports.

## Development verification

```sh
pnpm install --frozen-lockfile
pnpm test
pnpm typecheck
pnpm build
pnpm exec wrangler d1 migrations apply cf-san-ip --local
(cd agent && go test ./... && go vet ./...)
sh agent/scripts/install_test.sh
```

This project does not scan an ASN, deploy the Worker, or mutate a remote D1 during local development. Follow the normal Cloudflare account/deployment process only after reviewing the code and configuring `JWT_SECRET`, D1, and `CF_SAN_RELEASE_REPOSITORY` in the target Worker environment.
