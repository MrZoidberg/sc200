# sc200

A Linux daemon for temperature-based fan control of Dell SC200 enclosures over serial. One process controls multiple ports independently and exposes cached Prometheus metrics and JSON health. Linux amd64 and arm64 are supported.

The initial protocol evidence is firmware `12Pack.Slot0.101`. Related firmware/models may share its console signatures; discovery is evidence of a compatible console, not model certification. Software tests do not qualify thermal limits or prove physical fan actuation. A disconnected cable, killed process, or failed host cannot enforce a safe speed.

## Install and first start

The self-contained [installer](https://github.com/MrZoidberg/sc200/blob/master/scripts/install.sh) can be copied to a gist unchanged; no gist has been published by this project workflow. Save and review `scripts/install.sh` from a trusted checkout, then on Linux:

```sh
sudo bash scripts/install.sh
# Or select an existing published release explicitly:
sudo bash scripts/install.sh --version vX.Y.Z
```

`vX.Y.Z` is a placeholder for a real release tag. Installation needs root, curl, GNU tar, sha256sum, account-management tools and systemd. It downloads GitHub release assets, verifies SHA-256 checksums from the same HTTPS release, and installs `/usr/local/bin/sc200`, a systemd unit, and tmpfiles policy. Checksums provide integrity, not an independent signature. No Go, git or jq is needed by the installer itself.

A fresh installation installs a disabled `/etc/sc200/config.example.yaml` and launches the Bubble Tea setup wizard on the controlling terminal (including `curl | bash`). The wizard discovers devices, lets you enable each enclosure, choose safe/quiet profiles or enter a custom fan curve and limits, then reviews your choices before saving. New devices start unchecked. On the review screen, optionally enable and start the service. Canceling leaves configuration unchanged. Without a terminal, or with installer `--no-setup`, control stays stopped; run `sudo sc200 --setup` later. Stop and disable your old `quite.sh` cron entry manually before discovery/control; the installer never edits cron. Inspect the crontab for the user that owns that job (`crontab -l` or `sudo crontab -l`), then use that user's `crontab -e` to disable only that entry.

Run `sudo sc200 --setup --config /absolute/path` to reopen setup. Stop an active daemon before setup so discovery can acquire the serial ports. Existing enclosure settings and global options are retained until you change them; custom mode edits minimum/fallback speeds, critical temperature, hysteresis, and the fan curve. Saving validates the complete configuration, preserves file ownership and permissions, and rejects concurrent edits. Selecting no enabled devices cannot be saved. The final service-start choice defaults to no.

```sh
# Preview: YAML on stdout, per-port outcomes on stderr; no config write.
sudo sc200 discover
# Restrict probing to known enclosure ports when other serial hardware exists.
sudo sc200 discover --port /dev/ttyS0 --port /dev/ttyS1 --write
sudo sc200 --check-config
# Review generated settings and commission conservatively before enabling:
sudo systemctl enable --now sc200.service
journalctl -u sc200.service -f
curl http://127.0.0.1:9700/api/v1/health
```

Discovery prints a YAML preview to stdout and diagnostic messages to stderr; it saves configuration only with `--write`. Automatic scans summarize failed or nonmatching ports, including unused UART device nodes. Use `sudo sc200 discover --verbose` for per-port failures, or `--port /dev/ttyS0` to probe a specific port (explicit ports always show failures).

Discovery uses bounded read-only `devils` and `_map` commands, never `set_speed`. It prefers `/dev/serial/by-id/` paths when available and merges new devices into valid existing YAML, preserving comments, settings, disabled and disconnected entries. Invalid existing YAML is rejected rather than reset. Review generated curves and sensor indices. Use `--profile quiet` to generate new entries with minimum 10%, fallback 15%, and a curve of 25°C→10%, 30→10%, 35→12%, 40→15%, 41→20%, 45→20%. The default `--profile safe` retains the conservative curve. Profiles populate explicit YAML and never overwrite existing enclosures. `safe_speed` is the startup/recovery/failure fallback, independent of the 100% maximum. Critical temperature (45°C by default) and fan faults request 100% with either profile. Stop a running daemon before probing its ports; advisory locks prevent discovery from borrowing daemon-owned ports. Restart an existing service after config changes; there is no hot reload.

All commands share `/etc/sc200/config.yaml`; `sc200 --config /absolute/path --check-config` and `sc200 discover --config /absolute/path --write` select another path without fallback. `sudo bash scripts/install.sh --config /absolute/path` installs the matching systemd override. `--serial-group GROUP` selects device permissions (default `dialout`); `--user NAME` changes the service account (default `sc200`). Ensure config and every parent directory are readable/traversable by that account. Newly created config directories are root owned, service-group owned, mode 2750; discovery files are mode 0640.

## Upgrade, rollback and removal

Run the installer again for latest or a pinned version. Existing configuration is preserved and validated by the new binary and service identity before replacement. A stopped service opens the setup wizard when a terminal is available (skip with `--no-setup`). An active service restarts after a validated upgrade; failed start/restart restores the previous binary/unit and attempts to restart the old active service. Inspect journald immediately after any failure. Keep backups of configuration and the previous trusted installer/release; deliberate downgrade uses `--version` with a compatible existing release. `--start` validates a real configuration before starting/enabling and cannot bootstrap a fresh absent config.

Before removal, arrange independent cooling and a controlled maintenance window. `sudo systemctl disable --now sc200.service` stops control and closes serial connections without changing fan speed. Independently observe fans/RPM; the firmware determines how long the last setting persists. If communication fails, restore cooling via a verified hardware procedure before removing control. After cooling is secured, remove the binary, unit and any `sc200.service.d/10-config.conf`, `/etc/tmpfiles.d/sc200.conf`, then `sudo systemctl daemon-reload`. Preserve `/etc/sc200` for recovery unless intentionally deleting it. Never unlink `/run/sc200_fan_control.lock` while any controller may hold it: replacement breaks inode-based exclusion. Account/group removal is optional only after checking other uses.

## Monitoring and further guidance

HTTP defaults to `127.0.0.1:9700`; `/healthz` is process liveness, `/readyz` and `/api/v1/health` return 503 when any enabled enclosure is unhealthy. `/metrics` exposes temperatures, RPM, freshness, targets, completed commands and bounded error counters. Startup/recovery begins at safe speed; default recovery requires three valid samples. Shutdown closes serial connections without sending a speed command.

Detailed source documentation: [operation/configuration/alerts](https://github.com/MrZoidberg/sc200/blob/master/docs/operations.md), [hardware evidence and capture](https://github.com/MrZoidberg/sc200/blob/master/docs/hardware.md), and [development/releases](https://github.com/MrZoidberg/sc200/blob/master/docs/development.md). These are source links; release archives include this standalone README, the binary, LICENSE, example YAML, unit and tmpfiles policy.

Service logs use plain text key/value fields in the systemd journal (`journalctl -u sc200 -f`). Successful fan speed changes include the enclosure, previous and new commanded percentages, and fresh sensor readings in `temperatures_celsius` as `sensor:degrees` pairs. Initial speed commands are also logged. Unchanged periodic refreshes are silent; missing or stale temperatures are marked `unavailable`. These events confirm successful commands, not measured physical fan speed; fan RPM remains available in metrics.

Set top-level `dry_run: true` in `/etc/sc200/config.yaml` and restart the service to poll real telemetry and evaluate the full policy without sending modification commands. The default is `false`. Startup, normal control, refresh, and failsafe speed requests are suppressed and logged with `dry_run=true`, the requested percentage, and fresh temperatures when available. Serial synchronization and read commands still run. Actual fan speeds remain under the enclosure’s existing control, including at shutdown. Simulated commands do not update completed-command metrics or counters; `sc200_dry_run`, `sc200_simulated_command_percent`, and the health JSON identify simulation. Set `dry_run: false` and restart to resume fan control.
