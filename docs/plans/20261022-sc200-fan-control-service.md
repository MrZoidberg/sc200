# Robust SC200 Fan Control Service

## Overview

Build `sc200`, a small Go daemon that replaces the periodic fixed-speed behavior of `quite.sh` with continuous temperature-based fan control. One Linux process manages multiple Dell SC200 enclosures through separate serial ports. Optimize for quiet operation with conservative thermal safeguards, independent failure handling, Prometheus metrics, a JSON health API, YAML configuration, device discovery that populates configuration, and systemd installation.

Deliver Linux release archives through GoReleaser, GitHub Actions for testing and tagged releases, golangci-lint, strong deterministic unit coverage, and a standalone installer suitable for copying to a gist. E2E tests are explicitly out of scope. This document plans implementation; it does not publish releases, install software on the user's host, or create a gist.

## Context (from discovery)

- Repository: `github.com/MrZoidberg/sc200`; currently a minimal `README.md`, MIT license, Go-oriented `.gitignore`, and an untracked `quite.sh`. Preserve that user-owned script without modifying or staging it implicitly.
- `quite.sh` defaults to `/dev/ttyS0`, 38400 baud, `set_speed 15\r`, three writes five seconds apart, a two-second response wait, and `/var/run/sc200_fan_control.lock`. It does not monitor temperature or verify fan behavior.
- The script's serial settings omit `-echo`. During discovery this produced a host-side echo feedback loop. The daemon must explicitly disable terminal echo and canonical processing.
- The linked command reference describes another firmware: `_temp_rd` is rejected by the user's `12Pack.Slot0.101` firmware and absent from its extended help. The user's successful `_map` transcript is the initial supported monitoring format. `set_speed <0–100%>` appears in the actual device's help; its response and persistence have not yet been captured.
- `_map` contains Type 04 temperature status and threshold rows, Type 03 fan rows, and unrelated sections. Each section starts with an aggregate row, followed by element rows. The supplied sample has four temperature sensors and four fans. Do not hard-code those counts for every enclosure.
- Sources: [command reference](https://pastebin.com/C0y5eavr), user-provided `devils`, `_devils`, and `_map` transcripts in the planning conversation, and [upstream sg3_utils SES decoding](https://github.com/doug-gilbert/sg3_utils/blob/main/src/sg_ses.c). Use the latter to verify field semantics; do not add an `sg_ses` runtime dependency.
- Dependencies to select and pin during implementation: a maintained Go serial package supporting explicit Linux termios configuration and bounded I/O, a strict YAML decoder, Prometheus Go client, and `golang.org/x/sys/unix` as needed for Linux device locking. Verify current official documentation before choosing versions. Standard library for CLI, HTTP, logging, synchronization, and signals.
- No custom planning rules were returned. Working title and regular testing approach were selected when the user asked to proceed.
- Plannotator feedback: add discovery of connected SC200 devices with configuration generation, and consistent reasonable default configuration paths. Addressed below and in added Task 9.

## Development Approach

- **Testing approach:** regular, code first and tests within each task. Finish each task and pass its checks before moving on.
- Add or update meaningful unit tests for every code change, covering success, failure, and relevant boundaries. Infrastructure-only and documentation tasks validate their artifacts rather than inventing runtime code just to add tests.
- Use small packages and narrow interfaces at hardware/time boundaries; avoid a plugin system or a general hardware framework.
- Update this plan whenever implementation changes scope. Mark items immediately, record new work with ➕, and blockers with ⚠️.
- Preserve the hardware behavior required by `quite.sh` (port, baud, CR command termination), but intentionally replace its cron scheduling and fixed-speed policy. No automatic migration or edits to existing cron files.

## Testing Strategy

- Standard commands: `go test ./...`, `go test -race -coverprofile=coverage.out -covermode=atomic ./...`, `go vet ./...`, and `golangci-lint run`.
- Minimum statement coverage: 85% overall and 90% individually for configuration, protocol parser, fan policy, and controller packages. Include command wiring in overall coverage; avoid blanket exclusions. Use a small checked-in Go coverage checker reading the coverage profile, with its own unit tests.
- Table-driven tests use the user's transcript, malformed fixtures, a fake clock, scripted serial streams, fake openers/locks, and `httptest`. Timeouts and cancellation tests must finish within a bounded test deadline without depending on hardware or long sleeps.
- Test concurrent enclosure isolation, metric scraping, and shutdown with the race detector. Add parser fuzz targets and run their seed corpora in regular unit tests; extended fuzzing is optional local work.
- Installer checks use stubbed commands and a temporary filesystem, not live downloads, root changes, or a running systemd instance. These are component tests, not deployment/e2e tests.
- Explicitly skip e2e and real-hardware tests in CI. Hardware commissioning is documented under Post-Completion and is not presented as completed verification.

## Solution Overview

Use a single process with one worker per configured enclosure. Each worker owns its serial port, serializes commands, polls `_map`, evaluates a pure fan policy, periodically reasserts `set_speed`, and publishes an immutable cached snapshot. Workers never wait on each other's serial I/O. One HTTP server exposes snapshots and Prometheus metrics without accessing hardware during a request.

This is the direct fit for the requested Go service and multiple serial ports. Separate systemd instances would isolate processes but duplicate HTTP listeners and configuration; spawning `stty`/shell commands would simplify initial access but complicate bounded reads, command framing, and testing. Keep direct serial access in Go.

No runtime configuration editing API, remote fan-control API, config hot reload, disk SMART polling, SES-over-SCSI backend, web UI, container image, or package repository in v1. Restart to apply configuration changes.

## Technical Details

### Configuration and CLI

- Binary commands: `sc200` (run), `sc200 --check-config` (no serial access), `sc200 --version`, and `sc200 discover` (probe connected devices and preview generated YAML). `sc200 discover --write` creates or extends configuration on disk.
- Define `/etc/sc200/config.yaml` once as the shared default for startup, check-config, discovery writes, installer, and systemd. Support `--config PATH` for an explicit alternative in all commands that read/write configuration; show the resolved path in help and relevant errors. Never silently fall back to another file or the current directory. Permission errors explain the requested path. Version/help do not require an existing configuration file.
- Strict YAML: reject unknown fields and duplicate keys. Require unique enclosure IDs and ports, at least one enabled enclosure, positive finite durations, supported baud rates, strictly increasing temperature points, nondecreasing percentages, and percentages within configured limits and 1–100. Resolve aliases and detect duplicate device identities at runtime as well as duplicate configured paths.
- Global HTTP bind defaults to `127.0.0.1:9700`; structured logs go to stderr/journald. Enclosure entries own port, baud, polling/command/staleness timing, curve, minimum speed, critical temperature, hysteresis, cooldown, recovery count, safe speed, and refresh interval.
- Initial proposed timing: poll 10s, command timeout 2s, sample stale after 30s, command refresh 30s, cooldown 30s, three valid recovery samples, reconnect backoff 1s doubling to 30s. Validate timeout/poll/stale relationships and bound complete transactions so a worker cannot silently stop checking staleness.
- Safe speed defaults to 100%; shutdown speed is 100%. A configured safe speed cannot be below the maximum curve speed. Example curve: 25°C→20%, 30→30%, 35→50%, 40→75%, 45→100%; critical 45°C, minimum 20%, hysteresis 2°C. These are initial software settings, not hardware-qualified thermal limits; documentation requires tuning against actual enclosure sensors and workload.
- Default temperature selection uses all discovered installed temperature sensors. Remember expected installed sensor identities after the first complete reading; a disappearing/unavailable required sensor triggers fallback rather than silently reducing the sensor set. Config may explicitly identify required sensor indices for a known installation. Do not permit an empty required set.
- Startup configuration validation fails before launching any worker or opening a port. Unavailable ports after validation degrade only their enclosure.

### Device discovery and configuration generation

- `sc200 discover` enumerates Linux serial candidates using the selected library/sysfs, preferring stable `/dev/serial/by-id/` paths and deduplicating underlying devices. Include physical UARTs such as `/dev/ttyS0` and USB serial adapters; exclude virtual consoles and PTYs. Repeated `--port PATH` restricts the candidate list for installations where only specified ports should be probed. Default baud is 38400; `--baud` supports an explicit override.
- Use the same raw/no-echo configuration, locking, absolute command deadlines, prompt framing, and size limits as the daemon. Probe only bounded read-only identification/sensor commands (`devils` and `_map`); never send `set_speed`, modify firmware settings, or enable/start the service. Recognize supported SC200 console evidence and validate map shape; a serial device existing, an arbitrary prompt, or a generic SES-like response alone is insufficient identity evidence. Document that related firmware/models may share these signatures; report uncertain devices without adding them automatically.
- One busy, inaccessible, timed-out, or unsupported port does not suppress valid discoveries. Report candidate outcomes to stderr and generated YAML to stdout; return a distinct nonzero result when no supported enclosure is found. Limit probe concurrency and total scan time, make cancellation prompt, and release ports/locks afterward. Respect daemon ownership; do not interfere with a running controller.
- Populate unique deterministic enclosure IDs, selected stable paths, baud, required installed temperature sensor indices, and the same conservative curve/timing/HTTP defaults used by the config loader. Generated entries are enabled so a reviewed generated config can pass check-config; generating/writing it does not start control. Do not infer sensor physical locations or qualify a curve from one snapshot.
- Without `--write`, preview candidate merged YAML without changing files; tolerate an absent destination file. With `--write`, create the default config directory/file when absent or append only newly discovered devices to an existing valid config. Preserve existing enclosure IDs, settings, global values, disabled entries, and comments; deduplicate by canonical device identity rather than path spelling. Invalid existing YAML or conflicting identities abort the write rather than resetting configuration. Do not remove entries for disconnected devices.
- Validate the complete merged config and write atomically with appropriate permissions. Serialize config writers, detect concurrent external edits before replacement, and leave the original intact on errors. A repeat scan/write is idempotent. No discoveries means no file replacement. No hot reload: report that an already running service must be restarted to use additions.

### Serial protocol and SES parsing

- Configure 38400 baud by default, 8N1, raw mode, no host echo or software/hardware flow control. Use CR-terminated commands. Never depend on shelling out to `stty` in production.
- Acquire the legacy `/run/sc200_fan_control.lock` for compatibility with the existing script, plus per-device locks/exclusivity for each actual serial device. Keep locks through bounded shutdown writes. Report conflicts explicitly. Prevent aliases from controlling one device twice. Document that advisory locks cannot stop unrelated software that ignores them.
- Synchronize to an observed `12Pack... >` prompt, with bounded startup drain. Handle command echoes, CR/LF variants, split reads, unsolicited lines, prompt counters, partial writes, transport failures, response size limits (initial cap 64 KiB), and absolute transaction deadlines. After timeout discard/resynchronize before accepting another response; never treat late data as a fresh reading.
- The only control-path commands are `_map` and validated integer `set_speed N`. No automatic thermal threshold changes, synthetic sensor overrides, watchdog changes, firmware operations, or arbitrary configured command strings.
- Parse `_map` by section type and row shape, not by scanning arbitrary hex text. Exclude aggregate rows by position. Preserve element indices, status codes, status flags, and raw values. Ignore well-formed unrelated section types; reject malformed relevant sections and incomplete transactions.
- SES temperature: third status byte minus 20°C; zero is unavailable, not −20°C. The four sample bytes `32`, `34`, `33`, `29` yield 30, 32, 31, 21°C. Evaluate status and failure/overtemperature flags before using a value. Threshold bytes are separate metadata, not current temperatures.
- Cooling: decode actual RPM from the SES speed field with its factor semantics; cross-check against upstream decoder. For factor-zero sample rows, `01 01 0C 20` yields 2680 RPM. Surface installed fan faults, unavailable readings, and stopped fans as unhealthy and trigger conservative fallback; distinguish explicitly noninstalled elements from failures.
- The original sample second power-supply status is SES 03 (noncritical), not SES 05 (not installed); preserve/report that fault rather than assuming an empty slot. Report relevant hardware faults without interpreting an intentionally empty PSU slot as a transport failure. V1 needs temperature/fan decoding, not complete electrical telemetry.
- A prompt without an explicit firmware error indicates only command transaction completion. It does not prove the requested percentage was applied. Capture real `set_speed` output before making stronger acknowledgement claims. Keep target percentage, last completed command percentage, and measured RPM distinct; a percentage cannot be inferred reliably from RPM alone.

### Control loop and failure states

- Startup and every reconnection: request safe speed before allowing any low-speed setting. Remain in `starting` until complete valid telemetry and successful control transactions establish readiness. Require three consecutive valid samples before leaving fallback/startup.
- Use the maximum required sensor temperature with piecewise-linear interpolation, round upward to an integer percentage, and clamp to configured bounds. Temperature rises increase speed immediately. Reductions require the lower target to remain stable for the cooldown and satisfy the 2°C hysteresis test against the temperature that last justified the retained speed; evaluate reductions using `curve(current_temperature + hysteresis)` to avoid crossing back and forth. Define these rules as a pure function and test ramps and boundaries.
- Critical configured temperature or sensor overtemperature flags override all hysteresis/cooldown and request 100%. Installed fan failure also overrides quiet operation. Do not smooth or average away a hot sensor.
- Invalid/missing required sensors, stale telemetry, parse errors, unsupported commands, or failed control transactions mark the enclosure unhealthy immediately and request safe speed if transport permits. Staleness uses monotonic time and advances independently of successful serial transactions. Never retain low speed merely because one old reading was cool.
- Reassert the current target at the refresh interval even if unchanged, accounting for possible firmware override/expiry. Retry only within bounded budgets, then close and reconnect with capped backoff. A failed write or ambiguous response cannot update the completed-command field.
- States: `starting`, `healthy`, `failsafe`, `disconnected`, `stopping`, with stable reason codes and transition timestamps. Fresh samples alone do not recover a failed control path. Track last successful telemetry and command separately.
- A disconnected process cannot enforce safe speed on hardware. Log and expose an unsuccessful fallback attempt; never report safe speed as achieved. SIGKILL, host failure, and unplugged cables similarly exceed software guarantees. Do not assume firmware restores automatic control without verification.
- On SIGTERM/SIGINT stop normal polling, attempt 100% on all reachable enclosures concurrently within a shared bounded shutdown window (initially 5s), stop HTTP, close ports, and release locks. A bad enclosure must not delay the others. HTTP bind failure or other fatal process errors use the same cleanup path.

### Metrics and health contracts

- `GET /metrics`: Prometheus collector over cached snapshots. Include `sc200_temperature_celsius{enclosure,sensor}`, `sc200_fan_rpm{enclosure,fan}`, target and completed-command percentages, `sc200_enclosure_healthy`, `sc200_failsafe_active`, telemetry/command last-success Unix timestamps, bounded operation/error counters, and build info. Use bounded labels only; no error messages, device prompts, paths, or timestamps as labels. Omit unavailable/stale temperature and RPM samples rather than advertising them as current; freshness metrics explain absence.
- `GET /healthz`: process liveness, HTTP 200 while serving; enclosure failures do not create a restart loop.
- `GET /readyz`: 200 only when all enabled enclosures are healthy, otherwise 503.
- `GET /api/v1/health`: JSON process summary and per-enclosure states/reason codes, sensor validity, current readings or null, target/completed-command percentages, and last-success times; status 200 only when every enabled enclosure is healthy, otherwise 503. Do not expose raw serial responses.
- Configure HTTP read-header/write/idle timeouts, allow read-only methods, set JSON content type, and gracefully shut down. No authentication in v1; bind loopback by default and document deliberate network exposure for Prometheus scraping.

### Installation and delivery

- Go module `github.com/MrZoidberg/sc200`; release binary `sc200`; Linux amd64 and arm64 initially, `CGO_ENABLED=0` if supported by the selected serial implementation. Pin Go/tool versions consistently after checking their official compatibility requirements.
- GoReleaser archives include binary, LICENSE, example config, systemd unit, and installation documentation, with predictable artifact names and SHA-256 checksum file. Embed version/commit/build date via linker flags. No cross-platform daemon support claimed.
- `scripts/install.sh` is self-contained when copied to a gist: defaults to the fixed repository, supports latest or an explicit release version, maps supported Linux architectures, downloads assets to `mktemp`, verifies checksums before touching installed files, and cleans up via trap. Use curl with fail/HTTPS/redirect/time limits; do not require Go, git, jq, or repository checkout.
- Install binary atomically to `/usr/local/bin/sc200`, unit to `/etc/systemd/system/sc200.service`, and config to `/etc/sc200/config.yaml` only if absent. Preserve existing configuration on upgrade, validate it before restart, and restore the previous binary if a requested restart fails. A new install writes an example but does not start hardware control until configured; explain `sudo sc200 discover --write` as the discovery/setup path and avoid an enabled placeholder device in the example. `--start` enables/starts only after successful validation. Existing active installations restart after a successful validated upgrade. State this behavior in help text. The systemd unit and installer use the shared default path; document an explicit unit override for a custom `--config` path.
- Run as a dedicated `sc200` user with the serial device group (`dialout` by default on Debian/Proxmox; support an explicit group override). Establish `/run/sc200` via systemd RuntimeDirectory and allow the compatibility lock via narrowly scoped setup. Use `Restart=on-failure`, an adequate stop timeout, `NoNewPrivileges`, `ProtectSystem`, and `ProtectHome`; do not enable `PrivateDevices`, which hides serial ports. Test the unit's writable paths against lock creation and serial access.
- CI on pull requests and main-branch pushes runs format, vet, race/unit coverage gates, lint, installer checks, Linux cross-builds, GoReleaser config validation, and an unpublished snapshot build. A `v*` tag workflow repeats required checks for that commit before publishing through GoReleaser. Pin actions/tools, use read-only permissions except the release job's `contents: write`, and serialize release jobs. No release publication during implementation verification.

## What Goes Where

- **Implementation Steps:** repository code, unit tests, fixtures, packaging, CI, and documentation, tracked below.
- **Post-Completion:** physical hardware commissioning, gist publication, service activation, and live GitHub release verification. These require external actions and are not unit-test acceptance claims.

## Implementation Steps

### Task 1: Define and validate service configuration

**Files:**
- Create: `go.mod`, `go.sum`, `internal/config/config.go`, `internal/config/config_test.go`, `configs/sc200.example.yaml`

- [x] Initialize the module and pin Go/YAML dependencies; define strict global and per-enclosure configuration with documented defaults.
- [x] Implement validation for IDs, ports, durations, curves, sensor requirements, percentages, and safe shutdown/fallback policy.
- [x] Add tests for valid multi-enclosure configuration, shared config-path defaults, defaulting, and example configuration decoding.
- [x] Add tests for unknown/duplicate fields, duplicate IDs/ports, empty enabled set, invalid durations, NaN/Inf, nonmonotonic curves, and unsafe bounds.
- [x] Run `go test ./...`; pass before Task 2.

### Task 2: Parse SC200 sensor maps

**Files:**
- Create: `internal/protocol/map.go`, `internal/protocol/map_test.go`, `internal/protocol/testdata/map-12pack.txt`, `internal/protocol/testdata/unsupported-temp-rd.txt`

- [x] Preserve the supplied full `_map` transcript and initial unsupported-command response as fixtures; record firmware provenance without inventing missing output.
- [x] Implement section-aware temperature/fan decoding, aggregate exclusion, element status, fault flags, unavailable values, and threshold separation.
- [x] Add tests for sample temperatures/RPM, unrelated sections, variable element counts, absent PSU slots, and whitespace/line-ending variations.
- [x] Add tests for malformed/truncated rows, missing/duplicate relevant sections, invalid hex, zero temperature, non-OK statuses, speed factor boundaries, and overtemperature/fan-fault flags; add parser fuzz seeds.
- [x] Run `go test ./...`; pass before Task 3.

### Task 3: Implement bounded serial transactions and device ownership

**Files:**
- Create: `internal/serial/port_linux.go`, `internal/serial/lock_linux.go`, `internal/serial/session.go`, `internal/serial/session_test.go`, `internal/serial/lock_linux_test.go`

- [x] Select/pin the serial dependency; implement raw/no-echo 8N1 configuration, per-device exclusivity, compatibility lock handling, and alias detection.
- [x] Implement CR commands, bounded prompt synchronization/framing, deadlines, size limits, partial writes, error recognition, resynchronization, and cancellation.
- [x] Expose only sensor reads and validated speed writes through a small interface; preserve distinction between transaction completion and physical actuation.
- [x] Add tests with scripted ports for chunked prompts/echoes, successful maps, serialization, lock acquisition/release, and correct raw-mode flags through an injected configurator.
- [x] Add tests for busy/aliased devices, unsupported commands, I/O errors, stale queued responses, deadline/size limits, partial writes, and cancellation during reads; no physical port required.
- [x] Run `go test ./...`; pass before Task 4.

### Task 4: Implement the pure thermal fan policy

**Files:**
- Create: `internal/control/policy.go`, `internal/control/policy_test.go`

- [x] Implement maximum-sensor selection, interpolation with upward rounding, speed bounds, critical overrides, and safe-speed decisions.
- [x] Define explicit state for hysteresis, delayed reductions, and recovery samples; increases and emergency actions bypass reduction delays.
- [x] Add table-driven tests for exact points, interpolation, clamp boundaries, cold/hot ramps, hysteresis, cooldown, and recovery.
- [x] Add tests for missing/failed sensors, changing required sensor sets, temperature spikes, emergency transitions, fan faults, and repeated threshold oscillation using injected time.
- [x] Run `go test ./...`; pass before Task 5.

### Task 5: Run an enclosure control worker with reconnect and fallback

**Files:**
- Create: `internal/control/worker.go`, `internal/control/state.go`, `internal/control/worker_test.go`

- [x] Implement startup-safe requests, polling, successful-command tracking, periodic refresh, bounded retry/backoff, and reconnect-safe recovery.
- [x] Publish synchronized immutable snapshots with freshness and state/reason transitions; enforce staleness even when serial I/O fails.
- [x] Implement bounded cancellation and shutdown safe-speed attempts without retaining a low target after a telemetry/control error.
- [x] Add deterministic tests for startup→healthy, speed refresh, recovery after reconnect, and transition timestamps.
- [x] Add tests for valid telemetry with failed writes, stalled transactions, stale/partial samples, shrinking sensor sets, disconnected fallback failure, cancellation, and retry caps.
- [x] Run `go test -race ./...`; pass before Task 6.

### Task 6: Coordinate independent enclosure workers

**Files:**
- Create: `internal/service/manager.go`, `internal/service/manager_test.go`

- [x] Start one worker per enabled enclosure after full configuration validation; aggregate cached state and bound lifecycle cleanup.
- [x] Keep per-enclosure serial failures independent and coordinate concurrent shutdown with a shared deadline and lock release after final writes.
- [x] Add tests for multiple healthy enclosures, disabled entries, snapshot isolation, and orderly shutdown.
- [x] Add tests proving one permanently blocked/disconnected port cannot delay another enclosure's polling or shutdown; test startup conflicts and simultaneous cancellation under race detection.
- [x] Run `go test -race ./...`; pass before Task 7.

### Task 7: Expose Prometheus metrics and JSON health endpoints

**Files:**
- Create: `internal/httpapi/server.go`, `internal/httpapi/metrics.go`, `internal/httpapi/server_test.go`, `internal/httpapi/metrics_test.go`

- [x] Pin Prometheus dependency and implement collector metrics from snapshots with bounded labels and explicit telemetry freshness.
- [x] Implement `/healthz`, `/readyz`, `/api/v1/health`, method handling, server timeouts, and graceful shutdown.
- [x] Add `httptest` and Prometheus collector tests for healthy multi-enclosure responses, metric units/labels, and build info.
- [x] Add tests for startup/fallback/disconnection, stale sample omission/null JSON, partial-enclosure failure, unsupported methods, server errors, and concurrent scrape/state updates with no serial calls.
- [x] Run `go test -race ./...`; pass before Task 8.

### Task 8: Wire the CLI and process lifecycle

**Files:**
- Create: `cmd/sc200/main.go`, `cmd/sc200/main_test.go`, `internal/app/run.go`, `internal/app/run_test.go`, `internal/buildinfo/version.go`, `internal/buildinfo/version_test.go`

- [ ] Wire configuration, manager, HTTP, structured logging, version metadata, and injected signal context into a testable run function.
- [ ] Implement check-config/version/help without hardware access; return clear errors and exit statuses.
- [ ] Route fatal HTTP/startup errors and OS cancellation through bounded cleanup; keep transient device outages nonfatal.
- [ ] Add tests for CLI flags, version, default/explicit config paths, valid configuration check, and successful process start/stop with fakes.
- [ ] Add tests for bad flags/config, occupied HTTP listener, permission/lock errors, failed shutdown writes, and ensuring invalid config opens zero devices.
- [ ] Run `go test -race ./...` and Linux builds; pass before Task 9.

### Task 9: ➕ Discover connected enclosures and populate configuration

**Files:**
- Create: `internal/discovery/discovery_linux.go`, `internal/discovery/discovery_test.go`, `internal/config/write.go`, `internal/config/write_test.go`, `internal/protocol/testdata/devils-12pack.txt`
- Modify: `internal/app/run.go`, `internal/app/run_test.go`, `internal/config/config.go`, `internal/config/config_test.go`

- [ ] Implement candidate enumeration, stable-path preference, canonical identity deduplication, explicit port/baud overrides, bounded read-only identification/map probing, and per-port outcome reporting.
- [ ] Add `sc200 discover` YAML preview and `sc200 discover --write` using the shared default config path; generate unique IDs, discovered required sensors, and conservative loader defaults without starting control.
- [ ] Implement validated atomic create/merge preserving existing values/comments and disconnected/disabled entries, with config-write serialization, concurrent-edit detection, idempotency, and actionable permission errors.
- [ ] Add unit tests with fake enumeration/serial/filesystem boundaries for multiple discoveries, UART/USB devices, aliases, deterministic IDs, fresh generation, preview without writes, explicit/default paths, and repeated merges preserving user edits.
- [ ] Add tests for busy/unsupported/ambiguous devices, partial scan failures, timeouts/cancellation, no discoveries, invalid existing config, duplicate identities, concurrent edits, write failures, and ensuring probes issue zero control commands.
- [ ] Run `go test -race ./...`; pass before Task 10.

### Task 10: Package systemd operation and the standalone installer

**Files:**
- Create: `packaging/systemd/sc200.service`, `scripts/install.sh`, `scripts/install_test.sh`
- Modify: `configs/sc200.example.yaml`

- [ ] Implement the dedicated account/device permissions, runtime lock paths, restart policy, hardening, and bounded shutdown settings without hiding serial devices.
- [ ] Implement standalone latest/version installation, architecture mapping, checksum verification, safe extraction, atomic replacement, config preservation, validation, and upgrade rollback.
- [ ] Provide help and explicit first-install/discovery/start behavior with the shared config-path default; support serial-group override and avoid automatic cron edits.
- [ ] Add stubbed installer component tests for supported architectures, verified downloads, new install, upgrade preserving config, and requested start/restart command order.
- [ ] Add tests for unsupported OS/architecture, missing tools/root, unsafe versions/archives, failed downloads/checksums, validation/restart failure, rollback, and temporary-file cleanup; run `bash -n`, ShellCheck, and `systemd-analyze verify` on a suitable Linux runner.
- [ ] Run installer checks and `go test ./...`; pass before Task 11.

### Task 11: Automate quality checks and tagged releases

**Files:**
- Create: `.golangci.yml`, `.goreleaser.yaml`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `Makefile`, `scripts/checkcoverage/main.go`, `scripts/checkcoverage/main_test.go`
- Modify: `.gitignore`

- [ ] Define pinned tools and shared Make targets for unit/race tests, coverage gates, vet, formatting, lint, installer checks, and unpublished release validation.
- [ ] Configure static Linux amd64/arm64 archives, linker metadata, required packaging assets, and checksum names matching installer expectations.
- [ ] Add PR/push CI and tag releases with checks gating publication, minimal job permissions, and release concurrency protection.
- [ ] Add coverage-checker unit tests for passing/failing overall and package thresholds, malformed profiles, and missing expected packages.
- [ ] Validate workflow/config syntax against current official docs; run quality targets, GoReleaser config check, and snapshot build; assert archive contents, binary version, and checksum compatibility offline without publishing.
- [ ] Pass all checks before Task 12.

### Task 12: Verify acceptance criteria

**Files:**
- Modify as needed: tests in `internal/config/`, `internal/discovery/`, `internal/protocol/`, `internal/serial/`, `internal/control/`, `internal/service/`, `internal/httpapi/`, `internal/app/`, `cmd/sc200/`, and `scripts/`
- Modify: `docs/plans/20261022-sc200-fan-control-service.md`

- [ ] Verify monitoring, curves, conservative fallback, independent enclosure operation, discovery/config population, consistent default paths, health, metrics, service packaging, installer, CI, and release configuration against this plan.
- [ ] Add missing unit regression cases for discovered gaps, including success and error behavior; verify no code treats a queued old map or prompt-only response as fresh physical confirmation.
- [ ] Pass `go test -race -coverprofile=coverage.out -covermode=atomic ./...`, coverage gates, `go vet ./...`, formatting, and golangci-lint.
- [ ] Pass installer/unit-file checks, both Linux builds, and unpublished GoReleaser snapshot validation; confirm no e2e or physical-hardware dependency entered CI.
- [ ] Record measured coverage and checks; explicitly retain unverified hardware assumptions and external steps.

### Task 13: Finalize operator and contributor documentation

**Files:**
- Modify: `README.md`, `configs/sc200.example.yaml`, `docs/plans/20261022-sc200-fan-control-service.md`
- Create: `docs/hardware.md`, `docs/operations.md`, `docs/development.md`
- Move on completion: this plan to `docs/plans/completed/20261022-sc200-fan-control-service.md` (reuse an existing archive directory if one appears during implementation)

- [ ] Document supported firmware evidence, full transcript capture procedure with `-echo`, SES interpretation, unknown command behavior, acknowledgement limits, and shutdown/communication-loss limits.
- [ ] Document `sudo sc200 discover --write`, preview/restricted-port discovery, default and explicit config paths, safe merge behavior, and restarting after config additions; include multi-enclosure configuration, stable device paths, curve calibration, permissions, metrics/JSON contracts, alerts, and troubleshooting using real emitted names and fields.
- [ ] Document gist-ready installation, upgrades/rollback, version pinning, explicit first startup, disabling the old cron job, uninstall with a safe-speed procedure, and configuration restart requirements.
- [ ] Document local testing/coverage, tool versions, release tagging, and the distinction between unit-verified behavior and manual hardware commissioning.
- [ ] Validate documented config and CLI examples with existing tests; add/update example tests if documentation introduces different supported cases, and rerun relevant checks.
- [ ] Mark completed work and archive this plan only when implementation acceptance checks pass.

## Post-Completion

**Manual hardware commissioning:**

Capture `set_speed` response by reasserting the already intended operating percentage during a controlled maintenance session; observe RPM before/after and over time. Do not run the old cron controller alongside the daemon. Validate behavior across all attached firmware versions, sensor counts, populated PSU/fan layouts, reboot/reconnect, and override persistence. Compare decoded temperatures/RPM with an independent SES reading when available. Tune curves and required sensors under real disk load and sustained heat, beginning conservatively. Test normal service stop and reconnection; confirm what firmware actually does on loss of host control without claiming a software guarantee.

**External system updates:**

Publish `scripts/install.sh` to the user's gist when requested, then document its real URL. Configure host serial permissions and YAML, remove/disable the existing cron entry, activate the systemd service, and configure Prometheus scrape targets/alerts. After implementation is merged, create a version tag and verify the real GitHub release and installation on Linux; these external actions require their own authorization and are not necessary to validate unpublished release artifacts.
