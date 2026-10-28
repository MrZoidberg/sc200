# Operation

Follow the README installation sequence. `sc200 --help`, `sc200 discover --help` and `sc200 --version` do not require configuration or open serial ports. `sc200 --check-config` validates without hardware access. Exit statuses are 0 success, 1 runtime/configuration/output error, 2 usage error; discovery additionally returns 3 when no supported enclosure is found.

## Configuration and policy

`/etc/sc200/config.yaml` is the only implicit path, including discovery writes. There is no fallback or reload. The packaged disabled example intentionally fails validation until commissioned. For a compact valid two-enclosure configuration (replace port paths with your devices):

```yaml
http:
  listen: 127.0.0.1:9700
shutdown_timeout: 5s
enclosures:
  - id: shelf-a
    port: /dev/serial/by-id/usb-SHELF_A
    required_sensors: [0, 1, 2, 3]
  - id: shelf-b
    port: /dev/serial/by-id/usb-SHELF_B
    required_sensors: [0, 1, 2, 3]
```

Omitted enabled flags default true. Defaults are baud 38400, polling 10s, command timeout 2s, stale after 30s, refresh 30s, cooldown 30s, reconnect 1–30s, recovery samples 3, minimum 20%, safe speed 100%, critical 45°C, hysteresis 2°C. The curve is 25°C→20%, 30→30%, 35→50%, 40→75%, 45→100%. See `configs/sc200.example.yaml` for explicit fields. YAML rejects unknown/duplicate keys, duplicate IDs/ports and unsafe timing/curve values. Runtime canonical identity checks also reject aliases to the same device.

Discovery records installed temperature indices in `required_sensors`. For manual config, omission remembers all initially installed sensors; a subsequently disappearing/unavailable required sensor triggers fallback. Do not copy the four sample indices without checking your enclosure. Use the hottest required sensor; interpolation rounds upward. Rises act immediately. Reductions require cooldown and hysteresis; critical temperatures/flags and installed fan faults override quiet operation. `safe_speed` is the startup/recovery/failure fallback, not a maximum; curve speeds may range from `min_speed` to 100%. Critical temperatures and installed fan faults request 100%. These are initial software defaults, not certified hardware thresholds: begin conservatively, compare sensors with independent SES information, and test sustained disk workload/heat, reconnect and restart before lowering speeds.

`sc200 discover --baud 38400 --port /dev/ttyS0` previews merged YAML without writing. `--write` atomically creates/appends only new devices, validates the complete merge, deduplicates device identities and serializes writers. No discoveries leave the file unchanged; concurrent edits and invalid existing files abort. Preview may use an absent destination, but cannot merge into an invalid existing file. Default scans may touch other serial equipment: use explicit known ports. Run discovery with the daemon stopped, then validate as the service account and restart explicitly.

Generation profiles apply only to new entries: `sc200 discover --profile quiet --port /dev/ttyS0` previews the Quiet template; add `--write` to save. `--profile safe` is the default and uses the settings above. Unknown profiles fail before probing ports. Existing entries retain all their settings, even when discovered again with another profile. Generated YAML stores explicit values, with no runtime profile inheritance.

Quiet uses `min_speed: 10`, `safe_speed: 15`, critical 45°C, and the curve 25°C→10%, 30→10%, 35→12%, 40→15%, 41→20%, 45→20%. Other defaults are unchanged. Temperatures interpolate linearly and round upward; at 45°C the critical override requests 100%. Missing/invalid/stale telemetry falls back to 15%. Reductions use temperature + 2°C and wait for 30 seconds of a stable reduction target: after reaching 20%, cooling to 38°C permits 15% after cooldown. These are configurable operating targets, not hardware-qualified limits.

Service shutdown cancels polling and closes connections without sending a fan-speed command, in both normal and dry-run mode. Shutdown metrics count cleanup attempts and close errors. Firmware controls persistence of the last speed after the service exits.

For a custom config, use the installer's `--config` option, or create `systemctl edit sc200.service` with:

```ini
[Service]
ExecStartPre=
ExecStartPre=/usr/local/bin/sc200 --check-config --config /etc/sc200/custom.yaml
ExecStart=
ExecStart=/usr/local/bin/sc200 --config /etc/sc200/custom.yaml
```

Run `sudo systemctl daemon-reload`, validate `sudo -u sc200 sc200 --check-config --config /etc/sc200/custom.yaml`, then restart. Installer upgrades manage `10-config.conf`; pass the same custom path on subsequent installer invocations.

## Permissions and ownership

Default identity is `sc200:sc200` with supplementary `dialout`. Grant actual serial-device group access (udev rules may be needed for USB adapters), rather than making ports world writable. Custom config directories need traversal and read permissions for the service account; a root discovery file inherits the directory group only when setgid is configured. The unit provides `/run/sc200`; tmpfiles creates `/run/sc200_fan_control.lock` mode 0660. Keep that shared lock inode intact. Per-device exclusivity and advisory locks cannot stop unrelated software that ignores them. Stop old cron controllers and serial terminals before starting. `PrivateDevices` is intentionally absent; live systemd namespace permissions still require hardware commissioning.

## HTTP and metrics

Endpoints allow GET/HEAD; other methods return 405. `/healthz` returns 200 while serving even during device failures. `/readyz` and `/api/v1/health` return 200 only when every enabled enclosure is fresh and healthy; otherwise 503. They read cached data and never access hardware. There is no authentication: retain loopback binding, or deliberately restrict network access with firewall/proxy policy before remote scraping.

JSON top-level fields are `healthy` and `enclosures`. Each enclosure has `id`, `state`, `reason`, `healthy`, `fresh`, `transition_time`, `target_percent`, `completed_command_percent`, `last_telemetry`, `last_command`, `temperatures`, `fans`, and `power_supplies`. Reading objects have `index`, `valid`, `value` (Celsius for temperatures, RPM for fans); invalid/stale values are null. Unobserved last-success timestamps are null. State is `starting`, `healthy`, `failsafe`, `disconnected` or `stopping`. Reason explains the state; no raw serial transcript is exposed. Completed command percentage means transaction completion, never verified physical actuation.

Metric names and labels:

| Metric | Labels | Meaning |
| --- | --- | --- |
| `sc200_temperature_celsius` | enclosure, sensor | Valid fresh temperature |
| `sc200_fan_rpm` | enclosure, fan | Valid fresh measured RPM |
| `sc200_target_percent`, `sc200_completed_command_percent` | enclosure | Requested target / last completed command |
| `sc200_enclosure_healthy` | enclosure | Health as 0/1 |
| `sc200_failsafe_active` | enclosure | 1 only in failsafe state |
| `sc200_telemetry_fresh` | enclosure | Telemetry freshness as 0/1 |
| `sc200_telemetry_last_success_timestamp_seconds`, `sc200_command_last_success_timestamp_seconds` | enclosure | Unix time; 0 before first success |
| `sc200_operations_total`, `sc200_errors_total` | enclosure, operation | Counters; operation is open/read/command/fallback/shutdown |
| `sc200_build_info` | version, commit, date | Build metadata, value 1 |

Invalid/stale temperatures and RPM are omitted. A disconnected enclosure need not have `failsafe_active=1`; alert on health and freshness too. Example Prometheus scrape target: `127.0.0.1:9700` when Prometheus is local. Suggested alert expressions (choose durations to fit commissioning):

```promql
sc200_enclosure_healthy == 0
sc200_telemetry_fresh == 0
time() - sc200_command_last_success_timestamp_seconds > 60
increase(sc200_errors_total[5m]) > 0
up{job="sc200"} == 0
```

Configure the scrape job as `sc200` for the last expression. Alert separately on missing targets/series; absence of a temperature series is not a low temperature. Do not infer percent from RPM.

## Troubleshooting

Inspect `journalctl -u sc200.service` and JSON `reason`, `fresh`, last-success fields. A missing config is expected before first discovery; invalid config fails before opening ports. Permission failures need config-directory access, device group access and runtime lock setup checked as the service user. Busy ports require finding the other owner, not deleting lock files. No discoveries: restrict to the correct port/baud, check cable, permissions and supported console evidence. Unsupported `_temp_rd` is expected on the captured firmware; the daemon uses `_map`.

Readiness 503 during startup/recovery is expected until valid samples and successful commands establish health. Persistent fallback needs sensor indices, faults, stale readings and control-path errors investigated; fresh telemetry alone cannot recover a failed write. Failure of one enclosure does not stop the others. A stopped service leaves fan speed unchanged and requires independent cooling verification; restarting endlessly cannot guarantee hardware speed.

The health JSON `power_supplies` array retains each slot’s `index`, `installed`, numeric `ses_status`, and freshness-qualified `healthy` flag. An installed SES 03 supply reports a hardware fault; SES 05 identifies an empty slot. These statuses do not imply a transport failure or change the temperature control policy.
Runtime device and fallback errors include the enclosure identity in structured logs; identical errors are suppressed until recovery or a different failure.

Console logs use a compact local-time prefix, for example `Oct 28 13:56:26 INFO dry-run: fan speed command suppressed`, followed by structured `key=value` fields. Newlines in messages are escaped so each event occupies one line. Journald may add its own timestamp and service prefix.
