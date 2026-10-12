# Final-review fix report

Date: 2026-10-12
Reviewed range: `6e3f63e0a6f4407a356bcc626b78cf8362611c8f..2530c00a2b3602f82c4cb71201999509cd12fda7`

## Fixes

- Added an event-aware release-reference gate. `push` requires an exact stable `vMAJOR.MINOR.PATCH` tag, including rejection of prerelease/build-metadata and leading-zero forms; `workflow_dispatch` remains valid on branches. The build job only runs for tag pushes after the verification job succeeds.
- GitHub release `draft` and `prerelease` fields now require present, non-null JSON booleans. Missing, null, string, and numeric values fail closed with regression coverage.
- Added a root-only atomic pending-resume marker at `/var/lib/cf-san/update-resume` (injectable in tests). A successful update records intent before recovery; `Start` is followed by `Status` confirmation before clearing it. Daily recovery runs before release comparison, so a current installed version still retries recovery. Manual menu start/stop clears intentional pending state.
- Foreground `cf-san run` now registers its own PID under the shared update lock; menu/updater child starts use a managed path that does not wait on the lock held by their parent. Scanner start requires two consecutive live status observations, and updater restart confirms status after `Start`.
- Archive and checksum downloads now pass `--proto '=https' --proto-redir '=https'`, connect/overall/retry time bounds. Installer execution has a five-minute context deadline and kills its process group on timeout; recovery and lock-release tests cover the failure path.
- Added exported Go doc comments for the shared lock symbols. The scheduled wrapper explicitly invokes `auto-update --config /etc/cf-san/config.json`; custom config paths are documented as manual-only updater invocations.

## Verification commands and output

```text
$ go test ./... -count=1
ok  cf-san/cmd/cf-san
ok  cf-san/internal/autoupdate
ok  cf-san/internal/backend
ok  cf-san/internal/buildinfo
ok  cf-san/internal/config
ok  cf-san/internal/menu
ok  cf-san/internal/prefix
ok  cf-san/internal/probe
ok  cf-san/internal/scanner

$ go test -race ./... -count=1
ok  cf-san/cmd/cf-san
ok  cf-san/internal/autoupdate
ok  cf-san/internal/backend
ok  cf-san/internal/buildinfo
ok  cf-san/internal/config
ok  cf-san/internal/menu
ok  cf-san/internal/prefix
ok  cf-san/internal/probe
ok  cf-san/internal/scanner

$ go vet ./...
(exit 0; no output)

$ sh agent/scripts/install_test.sh
installer preflight checks passed

$ sh agent/scripts/install_mocked_test.sh
installer debian/amd64 install, update, pinning, wrapper, and idempotency checks passed
installer debian/arm64 install, update, pinning, wrapper, and idempotency checks passed
installer mocked integration checks passed for debian
installer debian/amd64 install, update, pinning, wrapper, and idempotency checks passed
installer debian/arm64 install, update, pinning, wrapper, and idempotency checks passed
installer mocked integration checks passed for debian
installer alpine/amd64 install, update, pinning, wrapper, and idempotency checks passed
installer alpine/arm64 install, update, pinning, wrapper, and idempotency checks passed
installer mocked integration checks passed for alpine
installer alpine/amd64 install, update, pinning, wrapper, and idempotency checks passed
installer alpine/arm64 install, update, pinning, wrapper, and idempotency checks passed
installer mocked integration checks passed for alpine
installer alpine/amd64 install, update, pinning, wrapper, and idempotency checks passed
installer alpine/arm64 install, update, pinning, wrapper, and idempotency checks passed
installer mocked integration checks passed for alpine
installer mocked distro matrix passed

$ pnpm test
Test Files  9 passed (9)
Tests       156 passed (156)
Test Files  4 passed (4)
Tests       109 passed (109)

$ pnpm typecheck
(exit 0; no output)

$ pnpm build
✓ built in 27ms
✓ built in 71ms

$ ruby -e 'require "yaml"; YAML.load_file(".github/workflows/agent-release.yml", aliases: true)'
workflow YAML parsed

$ sh -n agent/scripts/install.sh agent/scripts/install_mocked_test.sh agent/scripts/install_test.sh agent/scripts/validate_release_ref.sh && bash -n agent/scripts/install.sh agent/scripts/install_mocked_test.sh agent/scripts/install_test.sh agent/scripts/validate_release_ref.sh
(exit 0; no output)

$ git diff --check
(exit 0; no output)
```

Focused regression coverage also passed in the full Go suites for strict release booleans, pending resume on restart failure/current-version retry, unconfirmed `Start`, installer timeout process-tree cleanup, scanner-path lock release, foreground run lock behavior, manual-stop intent clearing, and real Manager lifecycle integration. The mocked curl fixture rejected downloads unless both HTTPS protocol restrictions were present.

## Residual limitations

- Native Debian cron/systemd and Alpine OpenRC service activation were not run on native init-system hosts; they remain mocked integration coverage.
- The arm64 cases are cross-architecture release fixtures in the Debian/Alpine matrix, not execution on arm64 hardware.
- GitHub Actions, a real Worker `/install` endpoint, a real GitHub Release, remote push/tag/release publication, and Worker deployment were intentionally not performed in this fix wave.

## Fresh final rerun output

```text
$ go test ./... -count=1
ok   cf-san/cmd/cf-san
ok   cf-san/internal/autoupdate
ok   cf-san/internal/backend
ok   cf-san/internal/buildinfo
ok   cf-san/internal/config
ok   cf-san/internal/menu
ok   cf-san/internal/prefix
ok   cf-san/internal/probe
ok   cf-san/internal/scanner

$ go test -race ./... -count=1
ok   cf-san/cmd/cf-san
ok   cf-san/internal/autoupdate
ok   cf-san/internal/backend
ok   cf-san/internal/buildinfo
ok   cf-san/internal/config
ok   cf-san/internal/menu
ok   cf-san/internal/prefix
ok   cf-san/internal/probe
ok   cf-san/internal/scanner

$ go vet ./...
(exit 0; no output)

$ pnpm test
Test Files  9 passed (9)
Tests       156 passed (156)
Test Files  4 passed (4)
Tests       109 passed (109)

$ pnpm typecheck
(exit 0; no output)

$ pnpm build
✓ built in 30ms
✓ built in 90ms

$ sh agent/scripts/install_test.sh
installer preflight checks passed

$ sh agent/scripts/install_mocked_test.sh
running mocked installer matrix: debian:bookworm-slim
installer debian/amd64 install, update, pinning, wrapper, and idempotency checks passed
installer debian/arm64 install, update, pinning, wrapper, and idempotency checks passed
installer mocked integration checks passed for debian
running mocked installer matrix: debian:trixie-slim
installer debian/amd64 install, update, pinning, wrapper, and idempotency checks passed
installer debian/arm64 install, update, pinning, wrapper, and idempotency checks passed
installer mocked integration checks passed for debian
running mocked installer matrix: alpine:3.18
installer alpine/amd64 install, update, pinning, wrapper, and idempotency checks passed
installer alpine/arm64 install, update, pinning, wrapper, and idempotency checks passed
installer mocked integration checks passed for alpine
running mocked installer matrix: alpine:3.22
installer alpine/amd64 install, update, pinning, wrapper, and idempotency checks passed
installer alpine/arm64 install, update, pinning, wrapper, and idempotency checks passed
installer mocked integration checks passed for alpine
running mocked installer matrix: alpine:3.23
installer alpine/amd64 install, update, pinning, wrapper, and idempotency checks passed
installer alpine/arm64 install, update, pinning, wrapper, and idempotency checks passed
installer mocked integration checks passed for alpine
installer mocked distro matrix passed

$ ruby -e 'require "yaml"; YAML.load_file(".github/workflows/agent-release.yml", aliases: true); puts "workflow YAML parsed"'
workflow YAML parsed

$ sh -n ... && bash -n ... && git diff --check
(exit 0; no output)
```
