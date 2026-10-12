# cf-san Daily Automatic Update Design

**Status:** Approved in conversation; awaiting written-spec review
**Date:** 2026-10-12
**Repository branch:** `main`

## 1. Goal and scope

Install an unattended daily update check for the Linux `cf-san` agent. When a newer GitHub Release is available, an active scan is gracefully paused, the verified release is installed, and scanning resumes. A scanner that was already stopped remains stopped. The update mechanism supports the existing Debian and Alpine targets and uses the configured Worker installer, preserving its release checksum verification and dependency-repair behavior.

This change includes the local agent command, installation/update scheduler integration, release version metadata, operational logs, and tests. It does not change scan planning, D1 schemas, API contracts, or the installer's trust/checksum model. It does not automatically update the Cloudflare Worker itself; the Worker must be deployed with the release so its `/install` endpoint serves the current installer.

## 2. User-visible behavior

- The installer enables one daily automatic check on Debian and Alpine. It is safe to rerun the installer and does not create duplicate jobs.
- The job checks the latest release for the configured cf-san GitHub repository. It performs no update if the installed version is current or newer.
- A small randomized delay is applied to the daily check so a fleet of agents does not all query or update simultaneously.
- If a newer release is found and scanning is active, the agent sends the existing graceful termination signal and waits for the scan process to exit and release its block leases before replacing the executable. After a successful update, it restarts the scan with the existing configuration.
- If scanning was stopped before the update, it stays stopped. A current-version check also leaves scanner state untouched.
- If the release check, installer download, checksum validation, or installation fails, the error is logged and the next daily run retries. If scanning had been active and the installed executable remains runnable, the job restarts it; it never launches a second scanner while the old process may still be alive.
- The existing manual menu update remains available. Automatic and manual updates must serialize so they cannot replace the binary concurrently.

## 3. Architecture

### 3.1 Version detection

Release builds embed both their semantic version (the Git tag, for example `v0.1.18`) and repository slug into the agent with linker flags. Add a non-interactive `cf-san version` command used for diagnostics. Development or unversioned builds must fail closed for automatic updates rather than treating an unknown version as old. The Worker release-repository setting must match the embedded repository slug; deployment verification checks this invariant.

The scheduled updater obtains the latest stable release tag from GitHub Releases for the embedded repository slug. It accepts only stable tags in `vMAJOR.MINOR.PATCH` form, validates the response, compares semantic versions, and only proceeds when the remote version is strictly newer. Network errors, malformed JSON, prereleases or invalid tags do not stop a running scan and do not trigger installation. The GitHub release lookup is unauthenticated and must not include the Worker API token.

### 3.2 Automatic update command

Add a root-only, non-interactive `cf-san auto-update` command. It loads the existing protected config, obtains a process-wide update lock, checks the remote version, and exits successfully without touching the scanner when no update is needed. Manual and automatic update paths share the lock and existing Worker `/install` update-only execution, including SHA-256 verification and dependency repairs.

For an actual update, the command records whether the scanner was active, signals it to stop, then polls its process state until it exits cleanly. A bounded shutdown timeout prevents an installer from replacing a binary still in use; on timeout, abort the update, log the condition, and do not start another scan. Once stopped, run the Worker-served installer with the existing URL/token environment contract. Do not print credentials in command output or logs. Preserve the configured ASN, scan limits, CPU setting, worker count, and agent ID.

On successful installation, restart scanning only if it had been active. On update failure, log the failure and attempt to resume the scanner only if it had been active and no scanner process is running. If the new binary cannot start, retain the error in the update log and allow the following daily check to retry; do not spin in a tight retry loop.

### 3.3 Daily scheduler and logs

Use the native daily scheduling mechanism already appropriate to each supported OS, installed idempotently:

- Debian: a cron entry invoking `cf-san auto-update` daily at 03:17 local host time.
- Alpine: an executable daily job in `/etc/periodic/daily/`, run by the system `crond` service.

The job adds a bounded randomized delay (up to 30 minutes) before checking. Installer setup ensures the required scheduler is available and enabled using the distro's normal service manager. If scheduling cannot be enabled, installation must report a clear error rather than claim automatic updates are active. Re-running install/update refreshes the job without duplicating it. The update command itself uses an exclusive OS-level lock to protect against overlapping scheduler/manual invocations.

Write outcomes to `/var/log/cf-san/update.log` with mode `0600`, separate from `/var/log/cf-san/agent.log`. Record timestamp, current/latest version, whether scanning was active, update result, and actionable error text. Never log the API token or full environment. Keep scheduler output redirected to this log and rotate it at a bounded size while retaining a small fixed number of root-only backups.

## 4. Failure and safety behavior

- Fail closed on unknown local version, invalid remote release metadata, unsupported release version, or inability to establish the update lock.
- Do not stop scanning until a strictly newer release has been verified from the trusted configured repository.
- Do not install while the scanner process is still alive. If graceful shutdown times out, abort and leave the process alone.
- Treat the installer checksum mismatch or any nonzero installer result as a failed update.
- Preserve scanner state: resume only when the scan was running before the update attempt. Never auto-start a deliberately paused or stopped scanner.
- A failed version check or update is retried on the next daily schedule; there is no rapid retry loop.
- Automatic update is disabled when the agent's release version is unknown; report a diagnostic in the update log.

## 5. Installer and deployment changes

The release workflow embeds the tag version in Linux `amd64` and `arm64` binaries and continues publishing the existing archives and `.sha256` files. The install script installs/enables the daily scheduler on both supported distributions, creates the update log with root-only permissions, and installs or refreshes the scheduled job. Existing credentials and scan configuration remain in `/etc/cf-san/config.json` with mode `0600`.

Deploy the Worker after the code is pushed and the release is built, because `/install` serves the install script from the Worker project. No D1 migration is required. Report GitHub commit push, tagged Release assets, and Worker deployment as separate completion states.

## 6. Acceptance and tests

- Version reporting is correct for tagged release builds and explicitly unknown for development builds.
- Semantic-version tests cover equal, newer, older, invalid, and unsupported remote tags.
- Mocked GitHub release tests cover HTTP errors, malformed JSON, prereleases, and verify that no API token is sent.
- Updater tests verify: no stop when current; no stop on lookup failure; active scan stops and is awaited before installer execution; active scan restarts after success; stopped scan remains stopped; failed update attempts resume only the formerly active scan; timeout never installs or starts a duplicate scanner; update lock excludes concurrent runs.
- Installer integration tests verify idempotent daily-job installation, log permissions, scheduler activation/error handling, and update behavior on Debian and Alpine.
- Existing Go, Worker, frontend, migration, and release workflow checks continue to pass. Do not perform a real network scan as part of tests.
- End-to-end release verification checks both architecture assets and their checksums, then verifies the deployed Worker `/install` response contains the updated daily scheduler installation logic.

## 7. Scope boundary

No automatic boot-time scan start is added. The daily updater resumes only a scan that was active when a verified newer version was about to be installed. No new D1 lease endpoint or schema is introduced; orderly agent shutdown must use the existing scanner cancellation and lease-release path.
