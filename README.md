# cf-san IP Pool

Cloudflare Workers + D1 IP pool with an authenticated admin UI, `/postip` and `/getip` APIs, and the `cf-san` ASN scanner agent.

## Agent scan scope and behavior

Only run `cf-san` against an ASN and IPv4 prefixes that you own or are explicitly authorized to assess. The agent asks RIPEstat for currently originating IPv4 prefixes, rejects non-public/mixed special-purpose ranges, splits eligible ranges into numerically ordered `/24` scan blocks (or keeps narrower blocks intact), and probes TCP/443. ZMap permutes addresses within one block; the durable checkpoint is per block, so a pause can repeat the active block but completed blocks are skipped on the next run. Each invocation completes one current prefix plan and then stops; start it again later to refresh RIPEstat.

`--limit` is the ZMap initial TCP probe rate in packets per second (default `100`), not a total interface shaper for subsequent TLS/HTTPS traffic. ZGrab2 concurrency is configured separately (`--senders`, default `100`). Set a conservative rate, follow the target network's acceptable-use policy, and pause promptly if asked. The agent never sends the API token to scan targets. HTTPS trace probes disable certificate verification only for the selected public IP; agent-to-Worker and agent-to-RIPEstat connections use ordinary TLS verification.

ZGrab2 first requires a successful TLS handshake; it is then followed by the direct `/cdn-cgi/trace` request. A trace response is accepted only when its HTTP headers contain `Server: cloudflare` (case-insensitive), its body does not contain `error code: 1034`, and it contains one usable, non-empty, whitespace-free `loc=` value. HTTP status is not itself a rejection. Missing `loc=` means the result is not saved. Valid candidates are immediately reported to D1; other probes only update block-level progress. A progress heartbeat is sent every ten minutes and when a block completes, with the latest known open-port/TLS target IP when available. This version does not apply a separate JA3/JARM or certificate-fingerprint allowlist; Cloudflare identity is decided by the agreed HTTPS response check. A heartbeat does not claim an exact last attempted IP because ZMap's in-block permutation and non-responsive hosts do not expose a reliable ordered cursor.

## Worker configuration for Agent installation

The authenticated UI's **安装 cf-san Agent** panel creates a command with `--url` and `--token`. It warns that the token may remain in shell history or be visible in local process arguments. The installer URL itself has no token query parameter, and the Worker does not log the token.

Set the non-secret Worker environment variable `CF_SAN_RELEASE_REPOSITORY` to the actual public GitHub repository in `owner/repository` form before using the installer panel. `/install` returns a clear `503` if this variable is missing or malformed. Do not put the API token in this variable. The variable must match the repository where the `v*` release workflow publishes assets.

The tag workflow runs the full Worker, UI, Go, migration, and installer checks. For a `v*` tag it creates:

- `cf-san_linux_amd64.tar.gz` and its `.sha256` file
- `cf-san_linux_arm64.tar.gz` and its `.sha256` file

Each archive contains the Go agent, the pinned ZGrab2 v1.0.0 binary, and its third-party license notice. ZMap is installed from the Debian/Alpine package manager on the target host. The installer checks the archive SHA-256 before installing either binary.

## Linux installation and menu

The generated command is intended for a Debian or Alpine Linux host with outbound HTTPS access and root/sudo privileges. It requires `curl` and a POSIX `sh` to fetch and run the script; the script installs `zmap`, CA certificates, and archive utilities, then installs `cf-san` and bundled `zgrab2` under `/usr/local/bin`.

Run `sudo cf-san` to open the text menu. It supports:

1. Initialize/check ZMap, ZGrab2, backend authentication, ASN, and the current scan plan.
2. Start a one-pass scan in the background.
3. Pause the scan with SIGTERM; the active block remains incomplete.
4. Set ZMap rate in packets per second.
5. Set ZGrab2 sender concurrency.
6. Set ASN.
7. View backend, ASN, rate, concurrency, and process status (the API token is not displayed).
8. Exit.

Local settings are stored at `/etc/cf-san/config.json`, owner-readable only (`0600`); credentials remain plaintext by design, so protect the host and backups. Runtime PID metadata is under `/run/cf-san`; logs are under `/var/log/cf-san`. The first version does not install a boot-time service.

CLI settings override saved values, for example:

```sh
sudo cf-san configure --url 'https://worker.example' --token 'YOUR_API_TOKEN' --asn 906 --limit 100 --senders 100
sudo cf-san run --config /etc/cf-san/config.json
```

`run` executes one current plan in the foreground. The menu starts it in the background and provides the pause control.

## Agent API

Both endpoints require `Authorization: Bearer <API_TOKEN>`:

- `GET /api/agent/v1/plan?asn=906` fetches and synchronizes the live authorized ASN plan.
- `POST /api/agent/v1/report` sends a block heartbeat/completion, optionally with one validated candidate. Candidate persistence and checkpoint update are batched together in D1.

These endpoints do not change existing `POST /postip` or `GET /getip` behavior. D1 stores scan history keyed by ASN and block; withdrawn prefixes disappear from the current plan but historical checkpoint rows remain.

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
