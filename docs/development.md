# Development and releases

The module is `github.com/MrZoidberg/sc200`. Supported runtime targets are Linux amd64 and arm64; builds use `CGO_ENABLED=0`. Go 1.27.1, golangci-lint 2.14.0, GoReleaser 2.18.2, actionlint 1.7.12 and ShellCheck 0.11.0 are pinned in the Makefile/workflows. `make tools` installs the Go tools into `.tools/bin`; install ShellCheck and systemd-analyze using the OS package manager. Linux/GNU tar is required for the installer and unit packaging checks. The macOS host can run many Go tests, but Linux-only serial/locking paths and coverage require a Linux runner.

```sh
make test
make race
make coverage
make fmt-check vet lint
make installer-check unit-check cross-build workflow-check
make snapshot
# Complete deterministic acceptance on a suitable Linux runner:
make quality
```

Coverage includes command wiring, with no blanket exclusions: minimum 85% overall and 90% each for configuration, protocol and control (policy/controller). `scripts/checkcoverage` reads `coverage.out` and has its own malformed/boundary tests. Tests use transcript fixtures, fake ports/time/ownership, cached HTTP collectors, and temporary-filesystem/stubbed installer commands. Parser fuzz seeds run with ordinary tests; optional extended fuzzing can target `internal/protocol`. No e2e or real-hardware tests are required by CI.

Acceptance before the documentation task measured Linux race coverage 92.76% overall, configuration 92.42%, protocol 97.33%, control 98.76%; these measurements are evidence for that implementation, not a permanent coverage guarantee. Required checks also include gofmt, vet, lint, installer component checks, actionlint, both static cross-builds, staged unit verification, unpublished GoReleaser snapshot/config checks and archive/checksum validation. Shell checks never activate a host service.

## Delivery

PRs and pushes to master run `.github/workflows/ci.yml`. The `v*` tag workflow repeats required checks for the tagged commit before release publication. Actions/tools are pinned; only the release job receives contents write permission and releases are serialized. `make snapshot` creates unpublished local artifacts and `scripts/checkrelease.py` verifies exact root members, SHA-256 entries and embedded metadata; native Linux binary execution is checked when the host can execute that architecture.

Archive names are `sc200_VERSION_linux_amd64.tar.gz` and `sc200_VERSION_linux_arm64.tar.gz` with `sc200_VERSION_checksums.txt` (VERSION omits the `v` tag prefix). Each archive contains exactly `sc200`, `LICENSE`, `sc200.example.yaml`, `sc200.service`, `sc200.conf`, `README.md`. Binary metadata is embedded through linker flags and visible in `sc200 --version`. Keep the README installation guidance standalone: linked source documents are not archive members, and both the installer and release checker enforce the six-file contract.

After review and merge, an authorized maintainer can select a real version, create an annotated `vX.Y.Z` tag on the accepted commit, push that tag and inspect the release workflow/assets. Tag/push/publish are explicit external actions; implementation verification has not performed them or verified live GitHub execution. Test a real release install on a suitable Linux host afterward. Publishing a gist, configuring production Prometheus, editing cron, installation and systemd activation are likewise separate operator actions.

Hardware commissioning remains mandatory: see [hardware.md](hardware.md). Unit tests establish decoding/control decisions and bounded failure behavior, not physical speed acknowledgement, firmware persistence, qualified thermal limits or live systemd device permissions.
