# cf-san ASN Scanner Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the existing Cloudflare Worker/D1 IP pool with an authenticated Go scanner, durable per-ASN `/24` checkpoints, a text menu, and a one-command Linux installer.

**Architecture:** The Worker resolves RIPEstat-originated IPv4 prefixes, filters public ranges, splits them into ascending `/24` scan blocks, persists block status in D1, and accepts authenticated heartbeat/candidate reports. The Go agent fetches this plan, runs ZMap TCP/443 followed by ZGrab2 TLS and a direct `/cdn-cgi/trace` request for each TLS responder, reports qualified results, and resumes only incomplete blocks. The existing React admin UI generates a token-bearing installer command; the Worker-served installer downloads a checksummed GitHub release bundle.

**Tech Stack:** Existing TypeScript/Hono/Cloudflare Workers/D1/React/Vite/Vitest project; Go standard library; ZMap; ZGrab2; Bash; GitHub Actions.

**Specs:** `docs/superpowers/specs/2026-10-10-ip-pool-worker-design.md` and `docs/superpowers/specs/2026-10-10-cf-san-agent-design.md`.

**Execution status (2026-10-11):** Implementation and post-implementation verification are complete. The initial pre-implementation red-test observations remain explicitly unchecked because they were not independently verified during this handoff. No production deployment or live ASN scan was performed.

## Global Constraints

- Keep existing `POST /postip` and `GET /getip` contracts unchanged.
- Agent endpoints use the existing configured `Authorization: Bearer <token>`; never send that token to scan targets or include it in URLs or Worker logs.
- Scan only live, public IPv4 prefixes originated by the configured ASN; reject invalid ASN, non-public ranges, and invalid/empty RIPEstat data before scanning.
- Split prefixes into `/24` blocks, retain a narrower prefix as one block, deduplicate overlaps deterministically, and sort blocks numerically ascending. ZMap may randomize addresses within a block.
- D1 stores progress by `(asn, block_cidr)`; incomplete blocks may be rescanned after pause/crash; completed blocks are skipped; a withdrawn prefix is omitted from the current plan but its history remains.
- Accept a candidate only when it belongs to the planned block, has the same ASN as the request, uses port 443, has a non-empty space-free `loc`, has `Server: cloudflare` case-insensitively, and its body does not contain `error code: 1034` case-insensitively. HTTP status alone is not a rejection criterion.
- Candidate IP upsert and its progress report must be submitted atomically. Heartbeats and invalid probes update progress only and never create records.
- `--limit` is ZMap packets/second, defaults to 100, and must be positive. ZGrab2 sender concurrency is a distinct setting.
- Send a progress report at least every ten minutes and on block completion. Persist block-level checkpoints; do not promise exact in-block IP resume order.
- Support Debian/Alpine on Linux amd64/arm64. The installer must verify checksums, set local configuration permissions to `0600`, and fail with actionable errors for unsupported platforms/dependencies.
- No production deployment, remote database mutation, or live network scan during development.
- No Git remote is configured in this worktree. Use an optional Worker variable `CF_SAN_RELEASE_REPOSITORY=owner/repository`; `/install` must fail clearly until it is configured rather than inventing a repository owner.

## File Structure

- `migrations/0002_scan_blocks.sql`: durable per-ASN block checkpoint table and indexes.
- `src/shared/ipv4-prefixes.ts`: strict IPv4 CIDR parsing, public-prefix filtering, numeric ordering, and `/24` subdivision.
- `src/worker/services/ripe-prefixes.ts`: bounded RIPEstat request and response validation.
- `src/worker/repositories/scan-blocks.ts`: D1 plan synchronization, ordered reads, checkpoint writes, and atomic candidate-plus-progress batch.
- `src/worker/routes/agent.ts`: Bearer-protected plan/report API.
- `src/worker/routes/installer.ts`: `/install` shell-script response, configured release repository validation, and no-store headers.
- `src/worker/env.ts`, `src/worker/index.ts`, `wrangler.jsonc`: environment binding, route registration, and worker-first routing for agent/install paths.
- `tests/shared/ipv4-prefixes.test.ts`, `tests/worker/agent-api.test.ts`, `tests/worker/installer.test.ts`: prefix, API, migration, authentication, atomic-write, and installer response tests.
- `src/client/components/AgentInstall.tsx`, `src/client/App.tsx`, `src/client/styles.css`, `src/client/api.ts`, `tests/client/App.test.tsx`: authenticated installer command, copy interaction, and token exposure warning.
- `agent/go.mod`, `agent/cmd/cf-san/main.go`: Go module and CLI entrypoint.
- `agent/internal/config/`: JSON config validation, safe read/write mode, and CLI overrides.
- `agent/internal/prefix/`: IPv4 block sorting and resume filtering.
- `agent/internal/backend/`: HTTPS Bearer client and typed plan/report messages.
- `agent/internal/probe/`: response validation and `loc` extraction.
- `agent/internal/scanner/`: ZMap/ZGrab2 subprocess orchestration, context cancellation, ten-minute heartbeat, candidate reporting, and block completion.
- `agent/internal/menu/`: text menu for initialization, run, pause, scan rate, sender count, ASN, and status.
- `agent/scripts/install.sh`, `agent/scripts/install_test.sh`: Debian/Alpine installer and mocked shell tests.
- `.github/workflows/agent-release.yml`: Go/ZGrab2 release builds, amd64/arm64 archives, checksums, tests, and tag publishing.
- `README.md`: agent authorization/rate-limit guidance, API protocol, installation configuration, menu, and verification instructions.

## Task 1: Add strict IPv4 block planning and D1 schema

**Interfaces:**

```ts
export interface PlannedPrefixBlock {
  asn: number;
  sourcePrefix: string;
  blockCidr: string;
}
export function planIPv4Blocks(asn: number, prefixes: string[]): PlannedPrefixBlock[];
```

- [x] Write tests for `/23 -> two ascending /24s`, `/24 -> itself`, `/25 -> itself`, overlap deduplication, IPv4 numeric ordering, invalid CIDR, invalid ASN, and rejection of private/reserved/multicast prefixes.
- [ ] **Not independently verified in final handoff.** Run `pnpm test:worker -- tests/shared/ipv4-prefixes.test.ts` and confirm the new exports/tests fail because they do not exist.
- [x] Implement `src/shared/ipv4-prefixes.ts` using `ipaddr.js`; return canonical CIDRs, deduplicate by `(asn, blockCidr)`, choose the narrowest canonical source prefix for overlaps, and sort by unsigned IPv4 network address then prefix length.
- [x] Add `migrations/0002_scan_blocks.sql` with `asn`, `block_cidr`, `source_prefix`, `status` constrained to `active|completed`, nullable `latest_ip`, `last_reported_at`, `created_at`, and `updated_at`; use `(asn, block_cidr)` as the primary key and an index for ordered ASN plan reads.
- [x] Run the prefix tests and local migration test; verify migration preserves all `ip_records` and `app_settings` rows.

## Task 2: Implement RIPEstat plan synchronization and authenticated report API

**Interfaces:**

```ts
GET /api/agent/v1/plan?asn=<positive-integer>
// 200 { asn: number, blocks: Array<{sourcePrefix:string, blockCidr:string, status:"active"|"completed", latestIp:string|null}> }

POST /api/agent/v1/report
// { asn:number, sourcePrefix:string, blockCidr:string, status:"active"|"completed", latestIp?:string, candidate?:{ip:string,port:443,asn:number,region:string} }
// 200 { ok:true }; malformed/out-of-scope reports are 400; unknown planned block is 404.
```

- [x] Add fixture-based tests for RIPEstat `data.prefixes.v4.originating` parsing, malformed/non-OK responses, no-prefix fail-closed behavior, canonical block order, Bearer auth, and required query/body validation.
- [ ] **Not independently verified in final handoff.** Run the new worker test and observe the missing route/service or repository failure.
- [x] Implement the RIPEstat client with a 10-second timeout and URL parameters `resource=<asn>`, `types=o`, `af=v4`, `noise=filter`, `list_prefixes=true`; reject non-2xx, malformed JSON, and empty filtered target sets.
- [x] Implement repository methods to insert new current blocks without resetting existing completed state, return only current-prefix blocks in numeric order, and retain historical rows for withdrawn prefixes.
- [x] Implement report validation against an already planned `(asn, block_cidr, source_prefix)` row; require candidate ASN to equal request ASN, port 443, global IPv4 membership in block, and non-empty space-free region.
- [x] Use a single D1 `batch()` for candidate upsert plus checkpoint update; for progress-only reports update only the checkpoint. Preserve an existing ASN alias when the same IP/ASN is refreshed, and clear it when an IP changes ASN.
- [x] Mount `/api/agent/v1/*` behind `createApiTokenMiddleware()` without changing `/postip` or `/getip`; keep injected fetch/repository seams testable.
- [x] Run agent API tests, existing public API tests, all worker tests, typecheck, and local migrations.

## Task 3: Add authenticated installer command UI and Worker installer route

**Interfaces:**

```ts
GET /install -> text/x-shellscript; charset=utf-8, Cache-Control: no-store
GET /api/admin/agent/install-command -> { command: string, warning: string }
```

- [x] Write Worker tests for missing/invalid `CF_SAN_RELEASE_REPOSITORY` returning a clear service error, valid repository returning a shell script with pinned asset naming and no token, and `/api/admin/agent/install-command` requiring JWT while shell-quoting URL/token safely.
- [ ] **Not independently verified in final handoff.** Run those tests and confirm failures before implementation.
- [x] Add optional `CF_SAN_RELEASE_REPOSITORY` to `Env` and Wrangler configuration; register `/install` in worker-first routing.
- [x] Implement `/install` to validate strict `owner/repository` syntax and serve the installer script with no-store/no-sniff headers; script accepts only `--url` and `--token`, never reads credentials from URL/query, and never echoes them.
- [x] Add admin API command generation using `context.req.url` origin plus the current D1 API token; return no command when the token or release repository is absent.
- [x] Add `AgentInstall` UI showing the assembled `curl | sudo sh -s -- --url ... --token ...` command and a clear shell-history/process-list warning; support copy and refresh after token rotation.
- [x] Add UI tests for command rendering, shell quoting, missing token, copy action, and warning; run client tests, typecheck, and build.

## Task 4: Implement Go config, plan client, prefix resume, and CF trace parsing

**Interfaces:**

```go
type Config struct { URL string; Token string; ASN uint32; Limit uint64; Senders uint32; AgentID string }
type Plan struct { ASN uint32; Blocks []Block }
type Block struct { SourcePrefix string; BlockCIDR string; Status string; LatestIP *string }
type Candidate struct { IP string; Port uint16; ASN uint32; Region string }
func ValidateCFResolveResponse(raw string) bool
func ParseTraceRegion(body string) (string, bool)
```

- [x] Add Go tests first for config validation and mode `0600`, CLI overrides, HTTPS-only backend URLs, plan numeric ordering, completed-block skipping, `loc` parsing (including missing/empty/space-containing values), and every provided Cloudflare response case.
- [ ] **Not independently verified in final handoff.** Run `cd agent && go test ./...` and observe the expected missing package/test failures.
- [x] Implement config persistence under `/etc/cf-san/config.json` (test-overridable), mode `0600`, default `--limit=100`, positive rate/senders validation, and flags `--url`, `--token`, `--asn`, `--limit`, `--senders`.
- [x] Implement HTTPS backend client with Bearer auth only to configured backend host, bounded request/response size, request timeout, typed plan/report JSON, and errors that never include the token.
- [x] Implement IPv4 numeric block ordering and skip `completed` blocks while preserving block-level resume behavior.
- [x] Implement `ValidateCFResolveResponse` to require case-insensitive `Server: cloudflare`, ignore status code, and reject body marker `error code: 1034`; implement strict `loc=` extraction without spaces.
- [x] Run Go unit tests and `go vet ./...`.

## Task 5: Implement ZMap/ZGrab2 scanner workflow and checkpoints

- [x] Add scanner tests using fake executable scripts for exact ZMap 443/rate/block args, ZGrab2 TLS/sender args, shuffled target ordering within a block, invalid TLS JSON, no `loc`, non-CF/1034 results, valid 404 acceptance, immediate candidate reports, progress heartbeat timing, completed-block reporting, and cancellation leaving active blocks incomplete.
- [ ] **Not independently verified in final handoff.** Run `cd agent && go test ./internal/scanner -v` and confirm the behavior tests fail before implementation.
- [x] Implement a scanner runner interface around `exec.CommandContext`; invoke ZMap once per block at configured `--limit`, send its successful IP stream to ZGrab2 TLS with configured `--senders`, parse successful TLS targets, and perform a direct HTTPS GET to `https://<ip>:443/cdn-cgi/trace` with bounded timeout/body and target-only certificate verification disabled.
- [x] Never attach backend credentials to ZMap/ZGrab2 arguments, target requests, URLs, or logs; use no redirects for target requests.
- [x] Report valid candidates immediately with `{port:443,asn,region}` and active-block progress; retry backend reports a bounded number of times and abort the active block if candidate persistence fails.
- [x] Run an independent ten-minute heartbeat while a block is active, reporting current block and latest known candidate IP; on block completion send `status=completed`; on context cancellation stop processes without completion.
- [x] Run scanner tests, Go full tests, and `go vet ./...`.

## Task 6: Add text menu, process control, and Debian/Alpine installer

- [x] Write menu and installer tests for initialize/connectivity check, run/pause, rate/sender/ASN settings, stale PID handling, distro/architecture detection, checksum mismatch, unsupported OS, and safe config permissions.
- [ ] **Not independently verified in final handoff.** Verify the tests fail before implementing those behaviors.
- [x] Implement `cf-san` default text menu and commands/flags for one-pass scanning; menu choices are initialize, run in background, pause gracefully, set ZMap PPS, set ZGrab2 senders, set ASN, show status, and exit.
- [x] Ensure one local scan process at a time; save process metadata under `/run/cf-san`, log under `/var/log/cf-san`, and terminate ZMap/ZGrab2 through context on pause/SIGTERM. No boot-time service is installed.
- [x] Implement `agent/scripts/install.sh`: require root; parse `--url` and `--token`; accept only Debian/Alpine with amd64/arm64; install `curl`, CA certificates, tar/gzip, and ZMap using apt/apk; download latest matching archive and checksum from the configured public GitHub repository; verify SHA-256; install `cf-san` and bundled `zgrab2`; write config atomically with mode `0600`; do not enable shell tracing or print secrets.
- [x] Run `bash -n agent/scripts/install.sh`, mocked installer tests, and Go tests.

## Task 7: Add release workflow, documentation, and final verification

- [x] Add `.github/workflows/agent-release.yml` to run worker/client tests, TypeScript typecheck/build, Go tests/vet, shell syntax/tests, build Linux amd64/arm64 agent and ZGrab2 release bundles, generate SHA-256 files, upload artifacts, and publish a GitHub Release on `v*` tags with least required token permissions.
- [x] Add third-party license notices for bundled ZGrab2 and test archive names/checksum verification.
- [x] Update `README.md` with setup of `CF_SAN_RELEASE_REPOSITORY`, tag/release flow, Debian/Alpine prerequisites, root requirement for ZMap, rate/concurrency distinction, authorization-only scope, menu operations, and backend endpoints.
- [x] Run `pnpm test`, `pnpm typecheck`, `pnpm build`, `pnpm exec wrangler d1 migrations apply cf-san-ip --local`, `cd agent && go test ./... && go vet ./...`, and `bash -n agent/scripts/install.sh` plus installer tests.
- [x] Inspect `git diff --check`, all changed files, and final status; verify no `.dev.vars`, database state, token, password, or build artifacts were staged/committed.

## Plan Self-Review

- Spec coverage: plan synchronization and fail-closed behavior (Tasks 1–2); authenticated reporting and atomic upsert/progress (Task 2); install endpoint/UI/credential warning (Task 3); CLI/config/menu/scanner/checkpoints/10-minute report/CF logic (Tasks 4–6); Debian/Alpine and amd64/arm64 release/checksums (Tasks 6–7); docs/tests and no production deployment (Task 7).
- Placeholder scan: no TODO/TBD implementation steps; release repository remains an explicit runtime configuration because the worktree has no Git remote.
- Type consistency: TypeScript APIs use `sourcePrefix`, `blockCidr`, `latestIp`; JSON wire mapping is documented in Task 2. Go types use corresponding exported CamelCase fields and are assigned JSON tags during implementation. Candidate uses port 443 and ASN equality across backend/agent.
