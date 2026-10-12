# cf-san Daily Automatic Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Install a daily updater for Debian and Alpine that checks the latest stable cf-san release, safely pauses an active scan, installs the verified release, and resumes only a scan that was active before the update.

**Architecture:** Embed the release tag and repository slug in tagged agent builds. A new `internal/autoupdate` package will query GitHub Releases, compare stable versions, serialize updates, orchestrate scanner stop/wait/install/restart, and write bounded private logs. The Worker-served installer remains the installation path; an optional exact-version argument pins automatic updates to the version that was checked. The installer configures Debian cron or Alpine's daily `crond` job idempotently.

**Tech Stack:** Go 1.x (from `agent/go.mod`), POSIX `sh`, GitHub Releases REST API, Debian cron, Alpine OpenRC/BusyBox `crond`, existing Worker installer, pnpm, Wrangler, GitHub Actions.

**Spec:** [2026-10-12-cf-san-daily-auto-update-design.md](../specs/2026-10-12-cf-san-daily-auto-update-design.md)

## Global Constraints

- Supported Linux distributions are Debian and Alpine.
- Stable release tags use exactly `vMAJOR.MINOR.PATCH`; prereleases and invalid tags are not installed.
- The daily random delay is bounded to 30 minutes.
- Scanner shutdown has a bounded wait; never replace the executable while its scan process is alive.
- Resume scanning only if it was active before a verified newer release was to be installed.
- Preserve `/etc/cf-san/config.json` and its `0600` permissions; do not log the API token or full process environment.
- Keep the existing release archive SHA-256 validation; no D1 migration or real network scan is part of this feature.
- Publish Linux `amd64` and `arm64` release assets, then deploy the Worker separately and verify `/install`.

---

## File and Component Map

- `agent/internal/buildinfo/buildinfo.go`: link-time `Version` and `Repository` identity, with a safe development default.
- `agent/internal/buildinfo/buildinfo_test.go`: stable-tag parsing/comparison and display tests.
- `agent/internal/autoupdate/release.go`: GitHub latest-stable-release client and release-tag validation.
- `agent/internal/autoupdate/installer.go`: fetch and validate the Worker installer, pass secrets only through environment, and request a pinned release when automatic updates supply a tag.
- `agent/internal/autoupdate/lock.go`: exclusive process lock shared by manual and automatic update paths.
- `agent/internal/autoupdate/runner.go`: version check, scan lifecycle, update, and conditional restart.
- `agent/internal/autoupdate/log.go`: append-only update diagnostics and bounded root-only rotation.
- `agent/internal/autoupdate/*_test.go`: HTTP, version, locking, lifecycle, failure, and log tests.
- `agent/internal/menu/process.go`: add bounded waiting for scanner termination after SIGTERM.
- `agent/internal/menu/menu.go` and `agent/internal/menu/updater.go`: retain manual update behavior and use the shared update lock/installer implementation.
- `agent/internal/menu/menu.go`: add `UpdateLockPath string` to `App`; an empty value resolves to `/run/lock/cf-san-update.lock`, while tests use a `t.TempDir()` path.
- `agent/internal/menu/updater_test.go`: retain menu integration coverage; move installer-specific unit tests to `internal/autoupdate`.
- `agent/cmd/cf-san/main.go`: add `version` and root-only noninteractive `auto-update` commands.
- `agent/cmd/cf-san/main_test.go`: verify `version` runs without reading a config file and a non-root caller is rejected before automatic-update work starts.
- `agent/scripts/install.sh`: install scheduler packages/services/jobs idempotently and accept an optional exact release tag in update-only mode.
- `agent/scripts/install_mocked_test.sh`, `agent/scripts/install_test.sh`: Debian/Alpine scheduled-job, permissions, idempotency, version pin, and unsupported-scheduler checks.
- `.github/workflows/agent-release.yml`: embed tag version and `${GITHUB_REPOSITORY}` in release builds; retain both architecture artifacts and checksum generation.
- `README.md`: describe daily update timing, scan pause/resume behavior, logs, disablement/removal, and failure behavior.

## Task 1: Embed build identity and expose the version command

**Files:**
- Create: `agent/internal/buildinfo/buildinfo.go`
- Create: `agent/internal/buildinfo/buildinfo_test.go`
- Modify: `agent/cmd/cf-san/main.go`
- Modify: `.github/workflows/agent-release.yml`

**Interfaces:**
- Produces `buildinfo.Version string` (default `dev`), `buildinfo.Repository string` (default empty), `buildinfo.Display() string`, and `buildinfo.CompareStableTags(a, b string) (int, error)`.
- `CompareStableTags` returns `-1`, `0`, or `1` for two valid stable `vMAJOR.MINOR.PATCH` tags and rejects all other forms.
- `cf-san version` prints version and repository without requiring the scanner config or root privileges.

- [ ] **Step 1: Write stable-tag tests first.** Cover `v1.9.0 < v1.10.0`, `v2.0.0 > v1.99.99`, equality, leading zeroes, missing `v`, prerelease suffixes, and non-numeric components.

```go
func TestCompareStableTags(t *testing.T) {
	got, err := CompareStableTags("v1.9.0", "v1.10.0")
	if err != nil || got != -1 {
		t.Fatalf("CompareStableTags() = (%d, %v), want (-1, nil)", got, err)
	}
}
```

Add `TestDisplay` cases for a tagged release (`cf-san v1.2.3 (owner/repo)`) and a development build (`cf-san development build`).

- [ ] **Step 2: Run the targeted test and confirm it fails because the package is not implemented.**

Run: `cd agent && go test ./internal/buildinfo -run TestCompareStableTags -count=1`

- [ ] **Step 3: Implement strict stable-tag parsing and numeric tuple comparison.** Keep `Version = "dev"` and `Repository = ""` as development defaults; do not silently normalize invalid tags.

```go
var stableTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func CompareStableTags(a, b string) (int, error) {
	left, err := parseStableTag(a)
	if err != nil {
		return 0, err
	}
	right, err := parseStableTag(b)
	if err != nil {
		return 0, err
	}
	for i := range left {
		if left[i] < right[i] {
			return -1, nil
		}
		if left[i] > right[i] {
			return 1, nil
		}
	}
	return 0, nil
}
```

- [ ] **Step 4: Add `version` command routing before config loading in `main.go`.** Print `cf-san <version> (<repository>)`; for development builds show `development build` and do not fail the version command.

```go
if command == "version" {
	fmt.Fprintln(os.Stdout, buildinfo.Display())
	return nil
}
```

- [ ] **Step 5: Pass release identity into tagged binaries.** In the release workflow's Go build command, set `-X cf-san/internal/buildinfo.Version=${GITHUB_REF_NAME}` and `-X cf-san/internal/buildinfo.Repository=${GITHUB_REPOSITORY}`. Add a native-amd64 release smoke assertion that the built binary's `version` output contains both values. Leave local/integration fixture builds on safe development defaults.

```sh
go build -trimpath -ldflags="-s -w -X cf-san/internal/buildinfo.Version=${GITHUB_REF_NAME} -X cf-san/internal/buildinfo.Repository=${GITHUB_REPOSITORY}" -o "$work/out/cf-san" ./cmd/cf-san
```

- [ ] **Step 6: Run buildinfo, CLI, and release-workflow checks.**

Run: `cd agent && go test ./internal/buildinfo ./cmd/cf-san -count=1 && go vet ./internal/buildinfo ./cmd/cf-san`

Run: `git diff --check && ruby -e 'require "yaml"; YAML.load_file(".github/workflows/agent-release.yml", aliases: true)'`

- [ ] **Step 7: Commit the tested component.**

```sh
git add agent/internal/buildinfo agent/cmd/cf-san/main.go .github/workflows/agent-release.yml
git commit -m "feat(agent): embed release identity"
```

## Task 2: Query and validate the latest GitHub release

**Files:**
- Create: `agent/internal/autoupdate/release.go`
- Create: `agent/internal/autoupdate/release_test.go`
- Use: `agent/internal/buildinfo/buildinfo.go`

**Interfaces:**
- `type Release struct { Version string }`.
- `type ReleaseClient interface { Latest(context.Context, string) (Release, error) }`.
- `GitHubClient.Latest` calls `https://api.github.com/repos/{owner}/{repo}/releases/latest` with a finite timeout, a fixed `Accept` header, and `User-Agent: cf-san-agent`; it sends no API token.
- Reject malformed repository slugs, non-2xx responses, oversized/invalid JSON, empty tags, invalid stable tags, and releases marked draft or prerelease.

- [ ] **Step 1: Add `httptest` cases for a valid latest release, 404/500, malformed JSON, empty tag, draft, prerelease, invalid version, and oversized response.** Assert the request path, `Accept`, and `User-Agent`; assert that no `Authorization` header is sent.

```go
func TestGitHubClientLatestDoesNotSendAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("release lookup unexpectedly sent Authorization")
		}
		_, _ = io.WriteString(w, `{"tag_name":"v1.2.3","draft":false,"prerelease":false}`)
	}))
	defer server.Close()
}
```

- [ ] **Step 2: Run the targeted release tests and verify the missing client causes failures.**

Run: `cd agent && go test ./internal/autoupdate -run TestGitHubClient -count=1`

- [ ] **Step 3: Implement the bounded HTTP client.** Production uses the constant GitHub HTTPS API base URL; inject an `httptest` server URL only through the test constructor/field, never through user config.

```go
request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
if err != nil {
	return Release{}, err
}
request.Header.Set("Accept", "application/vnd.github+json")
request.Header.Set("User-Agent", "cf-san-agent")
response, err := client.Do(request)
```

- [ ] **Step 4: Implement strict JSON and repository/tag validation.** Reuse `buildinfo.CompareStableTags` to validate release tags; accept only GitHub's latest stable release response.

```go
var payload struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}
```

- [ ] **Step 5: Run release tests and race checks.**

Run: `cd agent && go test ./internal/autoupdate -run 'TestGitHubClient|TestValidateRepository' -count=1 && go test -race ./internal/autoupdate -count=1`

- [ ] **Step 6: Commit the tested release client.**

```sh
git add agent/internal/autoupdate/release.go agent/internal/autoupdate/release_test.go
git commit -m "feat(agent): check latest stable release"
```

## Task 3: Pin Worker-installer updates to the checked release

**Files:**
- Create: `agent/internal/autoupdate/installer.go`
- Create: `agent/internal/autoupdate/installer_test.go`
- Modify: `agent/scripts/install.sh`
- Modify: `agent/scripts/install_test.sh`
- Modify: `agent/internal/menu/updater.go`
- Modify: `agent/internal/menu/updater_test.go`

**Interfaces:**
- `InstallFromWorker(ctx context.Context, cfg config.Config, version string, output io.Writer) error`; empty `version` means the current manual menu's latest-release behavior, while a nonempty version must be an exact validated stable tag.
- Worker script retrieval remains HTTPS and checks the script prefix/installer marker before execution.
- Installer script accepts `--version vMAJOR.MINOR.PATCH` only together with `--update-only`; it constructs `/releases/download/<tag>/...` URLs. Without `--version`, it retains `/releases/latest/download/...`.

- [ ] **Step 1: Move/copy installer-fetch and shell-execution tests into `internal/autoupdate`.** Add tests asserting that a pinned tag is passed as an argument, credentials are only environment variables, output does not contain the token, and malformed Worker responses are rejected.

```go
script := "test \"$1\" = --update-only; test \"$2\" = --version; test \"$3\" = v1.2.3"
```

- [ ] **Step 2: Extend shell preflight tests.** Assert `--version v1.2.3` succeeds only with `--update-only`, and rejects `latest`, prereleases, malformed tags, missing values, and an install-mode version pin.

- [ ] **Step 3: Implement URL selection in `install.sh`.** Keep archive SHA-256 verification unchanged; validate the optional tag before forming the URL, and never echo token values.

```sh
if [ -n "$UPDATE_VERSION" ]; then
  BASE_URL="https://github.com/${RELEASE_REPOSITORY}/releases/download/${UPDATE_VERSION}"
else
  BASE_URL="https://github.com/${RELEASE_REPOSITORY}/releases/latest/download"
fi
```

- [ ] **Step 4: Move the existing Worker installer fetch/execute logic into `internal/autoupdate/installer.go`.** Preserve HTTPS validation, 1 MiB installer-script limit, `--update-only`, `CF_SAN_BACKEND_URL`, and `CF_SAN_API_TOKEN` behavior.

```go
args := []string{"-s", "--", "--update-only"}
if version != "" {
	args = append(args, "--version", version)
}
cmd := exec.CommandContext(ctx, "sh", args...)
```

- [ ] **Step 5: Keep the menu manual updater as a wrapper around the shared installer.** Manual updates pass an empty version; automatic updates pass the checked stable tag.

- [ ] **Step 6: Run focused Go and shell tests.**

Run: `cd agent && go test ./internal/autoupdate ./internal/menu -count=1 && sh scripts/install_test.sh`

- [ ] **Step 7: Commit the pinned installer path.**

```sh
git add agent/internal/autoupdate agent/internal/menu/updater.go agent/internal/menu/updater_test.go agent/scripts/install.sh agent/scripts/install_test.sh
git commit -m "feat(installer): pin automatic updates to release tag"
```

## Task 4: Add safe process waiting and a shared update lock

**Files:**
- Modify: `agent/internal/menu/process.go`
- Modify: `agent/internal/menu/process_test.go`
- Create: `agent/internal/autoupdate/lock.go`
- Create: `agent/internal/autoupdate/lock_test.go`

**Interfaces:**
- `Manager.WaitStopped(ctx context.Context, interval time.Duration) error` polls the existing PID/executable validation until stopped or context expiry.
- `WithUpdateLock(path string, fn func() error) error` creates the lock parent directory, opens a `0600` lock file, uses an exclusive OS file lock, and returns `ErrUpdateInProgress` instead of overlapping another updater.
- Production lock path is `/run/lock/cf-san-update.lock`; tests use a temporary path.

- [ ] **Step 1: Add process tests for graceful SIGTERM followed by `WaitStopped`, an already-stopped process, and context timeout while a process ignores SIGTERM.**

```go
ctx, cancel := context.WithTimeout(context.Background(), time.Second)
defer cancel()
if err := manager.WaitStopped(ctx, 10*time.Millisecond); err == nil {
	t.Fatal("WaitStopped() succeeded while the test process was alive")
}
```

- [ ] **Step 2: Run the process tests and confirm `WaitStopped` is missing.**

Run: `cd agent && go test ./internal/menu -run 'TestManagerWaitStopped' -count=1`

- [ ] **Step 3: Implement context-aware polling using `Manager.Status()`; do not remove or weaken executable/PID validation.**

```go
for {
	running, err := m.Status()
	if err != nil || !running {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ticker.C:
	}
}
```

- [ ] **Step 4: Add lock tests for mutual exclusion, release after callback error, and reacquisition after the first callback returns.**

- [ ] **Step 5: Implement `WithUpdateLock` with `syscall.Flock(LOCK_EX|LOCK_NB)`, close the descriptor on every path, and make the path injectable.**

```go
if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return ErrUpdateInProgress
	}
	return err
}
defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
```

- [ ] **Step 6: Run focused race tests.**

Run: `cd agent && go test -race ./internal/menu ./internal/autoupdate -count=1`

- [ ] **Step 7: Commit safe wait and lock.**

```sh
git add agent/internal/menu/process.go agent/internal/menu/process_test.go agent/internal/autoupdate/lock.go agent/internal/autoupdate/lock_test.go
git commit -m "feat(agent): serialize updates and await scan shutdown"
```

## Task 5: Implement automatic-update lifecycle, jitter, and private logs

**Files:**
- Create: `agent/internal/autoupdate/runner.go`
- Create: `agent/internal/autoupdate/runner_test.go`
- Create: `agent/internal/autoupdate/log.go`
- Create: `agent/internal/autoupdate/log_test.go`
- Modify: `agent/internal/menu/menu.go`
- Modify: `agent/internal/menu/updater.go`
- Modify: `agent/internal/menu/menu_test.go`
- Modify: `agent/cmd/cf-san/main.go`

**Interfaces:**
- `type Scanner interface { Status() (bool, error); Stop() error; WaitStopped(context.Context, time.Duration) error; Start() error }`.
- `type InstallerFunc func(context.Context, config.Config, string, io.Writer) error`.
- `type Logger interface { Record(UpdateRecord) error }` and `OpenLogger(path string) (*UpdateLogger, error)`.
- `Runner.Run(ctx context.Context, cfg config.Config) error` owns a lock for the check/update lifecycle, checks that local version/repository are known, checks current version before signaling the scanner, and requests a pinned install only when the release is newer.
- `Runner` carries `CurrentVersion`, `Repository`, `Releases ReleaseClient`, `Scanner Scanner`, `Install InstallerFunc`, `Logger Logger`, `LockPath`, `ShutdownTimeout`, and `PollInterval`.
- `runAutomaticUpdate(cfg config.Config, configPath string, scanner autoupdate.Scanner) error` constructs the private logger and default runner in `main.go`.
- Shutdown deadline is 2 minutes, polled once per second; tests inject shorter durations.
- Update log is `/var/log/cf-san/update.log`, mode `0600`, rotates at 1 MiB and keeps three numbered backups.

- [ ] **Step 1: Write fake-based lifecycle tests before implementation.** Cover current/equal/older version (no stop), release lookup error (no stop), newer release with active scan (stop → wait → install exact tag → start), stopped scan (no start), install failure (resume only formerly active scan), shutdown timeout (no install/no duplicate), and competing lock (no lifecycle calls).

```go
if strings.Join(fake.events, ",") != "status,stop,wait,install:v1.2.3,start" {
	t.Fatalf("events = %v", fake.events)
}
```

- [ ] **Step 2: Run lifecycle tests and confirm they fail because the runner is absent.**

Run: `cd agent && go test ./internal/autoupdate -run TestRunner -count=1`

- [ ] **Step 3: Implement `Runner` with injectable release client, scanner, installer, lock path, shutdown timeout, and logger.** Acquire the lock immediately on command startup; scheduled jitter happens in the shell wrapper before the new executable starts, so the process always checks the version it was built with.

```go
return WithUpdateLock(r.LockPath, func() error {
	return r.checkAndUpdate(ctx, cfg)
})
```

- [ ] **Step 4: Implement update log append and bounded rotation.** Each record includes timestamp, current/latest version, prior scan state, outcome, and sanitized error text. Add tests for mode `0600`, rotation at 1 MiB, three backups, and token redaction.

```text
2026-10-12T03:17:05+08:00 current=v1.2.2 latest=v1.2.3 was_running=true result=updated
```

- [ ] **Step 5: Add `auto-update` CLI dispatch.** Require root, load existing config, configure manager arguments as `run --config <path>`, open the update log, invoke the runner, and return a nonzero exit on actual update/check failure. Add `version` and `auto-update` to unknown-command help.

```go
case "auto-update":
	if err := requireRoot(os.Geteuid()); err != nil {
		return err
	}
	manager := menu.NewManager()
	manager.Arguments = []string{"run", "--config", options.configPath}
	return runAutomaticUpdate(cfg, options.configPath, manager)
```

Test `requireRoot(1000)` returns an error and `requireRoot(0)` succeeds. Test `run([]string{"version", "--config", missingPath})` succeeds without reading `missingPath`.

- [ ] **Step 6: Make manual menu updates use `WithUpdateLock`.** Preserve the existing “scanner must be stopped” rule and leave menu numbering unchanged.

```go
return autoupdate.WithUpdateLock(app.updateLockPath(), func() error {
	return updater(context.Background(), *cfg, app.Output)
})
```

- [ ] **Step 7: Run full Go tests, race tests, and vet.**

Run: `cd agent && go test ./... -count=1 && go test -race ./... -count=1 && go vet ./...`

- [ ] **Step 8: Commit the tested updater lifecycle.**

```sh
git add agent/internal/autoupdate agent/internal/menu agent/cmd/cf-san/main.go
git commit -m "feat(agent): auto-update daily with scan recovery"
```

## Task 6: Install daily jobs on Debian and Alpine

**Files:**
- Modify: `agent/scripts/install.sh`
- Modify: `agent/scripts/install_mocked_test.sh`
- Modify: `agent/scripts/install_test.sh`

**Interfaces:**
- Installer writes `/usr/local/libexec/cf-san-auto-update`, a credential-free wrapper that sleeps for a random 0–1800 seconds and then execs `/usr/local/bin/cf-san auto-update`, redirecting output to `/var/log/cf-san/update.log`.
- Debian installs/enables cron and writes `/etc/cron.d/cf-san-auto-update` for root at 03:17 local time to invoke the wrapper; use `systemctl enable --now cron` when systemd is present, otherwise `update-rc.d cron defaults` and `service cron start`.
- Alpine installs an executable `/etc/periodic/daily/cf-san-auto-update` that invokes the same wrapper, and enables/starts `crond` through OpenRC.
- The cron and periodic jobs contain no backend URL or API token.
- Installer create/update paths share the same idempotent scheduler setup; failed package/service activation produces a nonzero, actionable error.

- [ ] **Step 1: Extend mocked installer tests with stub `systemctl`, `update-rc.d`, `service`, `rc-update`, `rc-service`, `od`, and `sleep` commands.** In the disposable distro containers, assert initial install and `--update-only` create exactly one job at `/etc/cron.d/cf-san-auto-update` or `/etc/periodic/daily/cf-san-auto-update`, install the wrapper mode `0750`, preserve job modes (`0644` and executable `0700` respectively), activate the scheduler, do not duplicate jobs, and never write the API token to either script.

```sh
test "$(grep -c 'cf-san-auto-update' "$CRON_FILE")" -eq 1
test "$(stat -c '%a' "$CRON_FILE")" = 644
```

- [ ] **Step 2: Run the installer integration harness and confirm new scheduler assertions fail before implementation.**

Run: `cd agent && sh scripts/install_mocked_test.sh`

- [ ] **Step 3: Add Debian cron package setup and scheduler activation.** Install the `cron` package on initial install and in update-only repair. Use `systemctl enable --now cron` when available; otherwise require both `update-rc.d` and `service`, enable the service at boot, and start it. If neither activation path works, fail rather than silently skip daily updates.

```sh
if command -v systemctl >/dev/null 2>&1; then
  systemctl enable --now cron
elif command -v update-rc.d >/dev/null 2>&1 && command -v service >/dev/null 2>&1; then
  update-rc.d cron defaults
  service cron start
else
  fail 'cannot enable the Debian cron service'
fi
```

- [ ] **Step 4: Add Alpine daily-runner installation and OpenRC `crond` activation.** Mark the periodic script executable and keep credentials out of it.

```sh
rc-update add crond default
rc-service crond start
install -m 0700 "$SOURCE_SCRIPT" /etc/periodic/daily/cf-san-auto-update
```

- [ ] **Step 5: Install a shared jitter wrapper and test its bounds without sleeping.** Generate an integer from `/dev/urandom` with `od`, map it to `0..1800`, call `sleep`, then `exec /usr/local/bin/cf-san auto-update`; stub `od` to values mapping to both 0 and 1800 and stub `sleep` to record the argument.

```sh
raw=$(od -An -N4 -tu4 /dev/urandom | tr -d ' ')
delay=$((raw % 1801))
sleep "$delay"
exec /usr/local/bin/cf-san auto-update >> /var/log/cf-san/update.log 2>&1
```

- [ ] **Step 6: Ensure update-only mode repairs scheduler setup on existing installations.** Create `/var/log/cf-san/update.log` with `0600` permissions. Preserve current Masscan/libpcap/cpulimit repairs, config migration, and archive checksum verification.

- [ ] **Step 7: Run shell syntax and mocked distro matrix tests.**

Run: `cd agent && sh scripts/install_test.sh && sh scripts/install_mocked_test.sh`

Expected matrix: Debian Bookworm/Trixie and Alpine 3.18/3.22/3.23, both amd64 and arm64 fixtures.

- [ ] **Step 8: Commit scheduler installation.**

```sh
git add agent/scripts/install.sh agent/scripts/install_mocked_test.sh agent/scripts/install_test.sh
git commit -m "feat(installer): schedule daily cf-san updates"
```

## Task 7: Document operations and verify end-to-end release and Worker deployment

**Files:**
- Modify: `README.md`
- Verify: `.github/workflows/agent-release.yml`, `wrangler.jsonc`, and Worker `GET /install`.
- Preserve: existing uncommitted valid-certificate changes in `agent/internal/probe/http.go`, `agent/internal/probe/http_test.go`, and `README.md`; do not revert them.

- [ ] **Step 1: Document the daily schedule, Debian/Alpine service requirements, random delay, update log path/rotation, scan-resume rules, manual pause/stop behavior, and how to disable the scheduled job.**

Document that disabling automatic updates means removing `/etc/cron.d/cf-san-auto-update` on Debian or `/etc/periodic/daily/cf-san-auto-update` on Alpine; do not stop the host's shared cron daemon.

- [ ] **Step 2: Run the complete repository verification suite.**

Run: `pnpm test && pnpm typecheck && pnpm build`

Run: `cd agent && go test ./... -count=1 && go test -race ./... -count=1 && go vet ./...`

Run: `cd agent && sh scripts/install_test.sh && sh scripts/install_mocked_test.sh`

Run: `git diff --check`

- [ ] **Step 3: Confirm Worker repository configuration matches the build's `GITHUB_REPOSITORY` slug.** `wrangler.jsonc` currently sets `CF_SAN_RELEASE_REPOSITORY` to `llovely45/cf-san-ip`; keep it aligned and do not add secrets.

- [ ] **Step 4: Commit documentation and any final integration fixes.**

```sh
git add README.md agent .github/workflows/agent-release.yml docs/superpowers/plans/2026-10-12-cf-san-daily-auto-update.md
git commit -m "docs: explain cf-san automatic updates"
```

- [ ] **Step 5: Create the next unused `vMAJOR.MINOR.PATCH` tag and push `main` plus the tag.** Verify the tag does not already exist before creating it. The tag triggers GitHub Actions; do not report a release until the workflow succeeds and both Linux assets plus `.sha256` files are present.

- [ ] **Step 6: Deploy the Worker with `pnpm deploy` after the release is published.** Verify the production `/install` endpoint returns the updated script containing scheduler setup and pinned-version support. Report Git push, release workflow/assets, and Worker deployment as separate states.

## Self-Review Checklist

- The design's no-update path, strictly-newer comparison, active scan stop/wait/lease release, bounded shutdown, pinned checksum-verified install, conditional restart, failure retry behavior, shared lock, daily jitter, private bounded log, Debian/Alpine scheduler installation, and Worker deployment are covered above.
- No D1 migration or API token is added to the release lookup or scheduler files.
- The existing valid-certificate change is preserved and included in the release verification without being conflated with the updater design.
