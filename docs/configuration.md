# Configuration reference

SC200 reads `/etc/sc200/config.yaml` by default. Use `sc200 --config /absolute/path` to select another file. Configuration changes require a service restart; there is no hot reload or fallback file.

This document covers every runtime YAML field. See [operations](operations.md) for installation, discovery, permissions, monitoring, and troubleshooting. The schema and defaults are defined in [config.go](../internal/config/config.go); control behavior is implemented in [policy.go](../internal/control/policy.go) and [worker.go](../internal/control/worker.go).

## Format and defaults

The file must contain exactly one YAML document. Unknown fields and duplicate keys are rejected. Omitted fields use the defaults below; `id` and `port` have no defaults. Explicit values must pass validation, including entries with `enabled: false`. At least one enclosure must be enabled.

Durations use Go duration strings, such as `500ms`, `2s`, `30s`, or `1m30s`. All duration fields must be positive. Temperatures are Celsius; speeds are integer percentages, not measured RPM. Temperature values and hysteresis must be finite numbers.

The following example explicitly shows every field. Replace the device path and sensor indices with values from your enclosure before use. The shipped [example](../configs/sc200.example.yaml) disables its enclosure deliberately; omitted `enabled` fields default to `true`.

```yaml
dry_run: false
http:
  listen: 127.0.0.1:9700
shutdown_timeout: 5s
enclosures:
  - id: enclosure-1
    enabled: true
    port: /dev/serial/by-id/usb-YOUR_ENCLOSURE
    baud: 38400
    poll_interval: 10s
    command_timeout: 2s
    stale_after: 30s
    refresh_interval: 30s
    cooldown: 30s
    reconnect_min: 1s
    reconnect_max: 30s
    recovery_samples: 3
    min_speed: 20
    safe_speed: 100
    critical_temperature: 45
    hysteresis: 2
    required_sensors: [0, 1, 2, 3]
    curve:
      - {temperature: 25, speed: 20}
      - {temperature: 30, speed: 30}
      - {temperature: 35, speed: 50}
      - {temperature: 40, speed: 75}
      - {temperature: 45, speed: 100}
```

## Global fields

| Field | Default | Meaning and constraints |
| --- | --- | --- |
| `dry_run` | `false` | When `true`, polls real telemetry and evaluates control policy, but suppresses all fan-speed writes, including startup, refresh, and fallback commands. Simulated requests are recorded in logs and health JSON, but are not exported as Prometheus metrics. Serial synchronization and reads still occur. Applies to every enclosure. |
| `http` | See below | HTTP server settings. |
| `http.listen` | `127.0.0.1:9700` | TCP bind address for health, readiness, JSON health, and Prometheus metrics. Requires a host/port address with a valid nonzero TCP port; IPv6 addresses need brackets, for example `[::1]:9700`. Endpoints have no authentication. Keep loopback binding unless network access is deliberately restricted. |
| `shutdown_timeout` | `5s` | Shared cleanup deadline supplied to the HTTP server and service manager during shutdown. Serial workers are canceled and connections closed without a speed command. Worker cleanup waits for canceled I/O to finish, so this value is not a strict upper bound on total process exit time. |
| `enclosures` | No entries | List of independently controlled enclosures. At least one entry must have `enabled: true`. Each entry accepts the fields below. |

## Enclosure identity and connection

All fields in this and the following tables are under `enclosures[]`.

| Field | Default | Meaning and constraints |
| --- | --- | --- |
| `id` | Required | Stable identifier used in logs, health JSON, and metric labels. Must start with an ASCII letter or digit and then contain only ASCII letters, digits, `_`, or `-`. Must be unique across all entries. |
| `enabled` | `true` | Enables this enclosure's worker. Disabled entries remain in the configuration and are validated, but are not controlled or included as active workers in readiness checks. |
| `port` | Required | Absolute serial-device path other than `/`, preferably a stable `/dev/serial/by-id/…` path. Cleaned paths must be unique, including disabled entries. Runtime device identity checks also reject aliases to the same device. The service account needs permission to access the device. |
| `baud` | `38400` | Serial baud rate. Allowed values: `9600`, `19200`, `38400`, `57600`, `115200`. Must match the enclosure console. |
| `reconnect_min` | `1s` | Initial delay before retrying a failed connection/control session. Retry delay doubles up to `reconnect_max`; a healthy policy sample resets it to this minimum. |
| `reconnect_max` | `30s` | Maximum reconnect delay. Must be greater than or equal to `reconnect_min`. |

## Polling, freshness, and recovery

| Field | Default | Meaning and constraints |
| --- | --- | --- |
| `poll_interval` | `10s` | Normal wait between control-loop iterations, after processing telemetry and any speed command. The worker may wake sooner for refresh or telemetry expiry, so this is not an exact fixed sampling period. |
| `command_timeout` | `2s` | Time budget for opening/synchronizing a session, reading telemetry, or sending a speed command. Reads and normal writes may receive a shorter budget to avoid crossing the telemetry freshness deadline. Must be less than `poll_interval`. |
| `stale_after` | `30s` | Maximum age of the last successful telemetry sample. At this age readings are stale, health fails, and the worker requests fallback and reconnects. Must satisfy `poll_interval + command_timeout < stale_after`. |
| `refresh_interval` | `30s` | Resends the current target after this much time since the last successful speed command, even when the target is unchanged. Target changes are sent without waiting for refresh. The loop reads telemetry before refreshing; this is a scheduling interval rather than an exact write deadline. Dry run tracks simulated commands separately. |
| `recovery_samples` | `3` | Number of consecutive new valid telemetry samples needed to regain healthy control after startup or failure, with a functioning control path. Must be at least `1`. Recovery uses `safe_speed`; normal reductions still require hysteresis and cooldown afterward. Faults and reconnects reset the recovery count. |

## Fan policy

| Field | Default | Meaning and constraints |
| --- | --- | --- |
| `min_speed` | `20` | Lower bound for normal curve targets. Integer from `1` to `100`; every curve point and `safe_speed` must be at least this value. |
| `safe_speed` | `100` | Fallback requested on startup, recovery, missing/invalid/stale telemetry, missing fans, or control/transport errors when communication permits. Integer from `min_speed` to `100`. It is independent of the maximum permitted curve speed. A failed connection cannot guarantee that this request reaches the enclosure. |
| `critical_temperature` | `45` | Emergency threshold: any installed, available temperature sensor at or above this value requests `100%`, even if it is not in `required_sensors`. An installed sensor's overtemperature flag also requests `100%`. Must be strictly above the first curve temperature and no higher than the last. |
| `hysteresis` | `2` | Nonnegative temperature margin used for reductions. The policy evaluates the curve at the hottest required temperature plus this margin and checks it against the temperature retained for the current target. Increases use the current temperature directly. See the example below. |
| `cooldown` | `30s` | How long an eligible reduction target must remain stable before lowering speed. A different reduction target restarts the timer; a blocked reduction or an equal/higher normal target clears it. Evaluated on telemetry samples, so the actual reduction can occur later than this duration. Increases and emergency overrides do not wait for cooldown. |
| `required_sensors` | Automatically selected | Optional list of unique nonnegative temperature sensor indices. Explicit lists must not be empty. Normal curve control uses the hottest listed sensor; a required sensor becoming absent, unavailable, or unhealthy triggers fallback. If omitted, the policy remembers all installed temperature sensors from its first complete sample and retains that set across reconnects. Restarting the process selects the set again. Discovery writes actual indices explicitly. |
| `curve` | Points below | List of at least two `{temperature, speed}` points. Temperatures must strictly increase; speeds must stay the same or increase. Each speed must be an integer from `min_speed` to `100`. Supplying a curve replaces the default list. |
| `curve[].temperature` | Per default point below | Finite Celsius temperature at this point on the curve. Fractional values are allowed. Together, the first and last points must bracket `critical_temperature` as described above. |
| `curve[].speed` | Per default point below | Integer requested speed percentage at the point. Intermediate temperatures use linear interpolation, rounded upward toward more cooling. Below/above the curve, the first/last point's speed applies, subject to emergency overrides. |

Default curve:

| Temperature (°C) | Speed (%) |
| --- | --- |
| 25 | 20 |
| 30 | 30 |
| 35 | 50 |
| 40 | 75 |
| 45 | 100 |

For example, the default curve requests `40%` at `32.5°C`. If the current target is `50%` and temperature falls to `30°C`, hysteresis evaluates `32°C`, producing a reduction target of `38%`. Assuming the retained-temperature check permits it, that target must remain stable for `30s` before the reduction occurs. Another decrease may follow after a new eligible cooldown. Hysteresis is a temperature margin, not a percentage margin.

An installed unhealthy fan also requests `100%` once required temperature sensors pass validation. Missing required sensors take the fallback path first; a missing fan set requests `safe_speed`. Emergency and failure paths reset recovery and pending reductions. No setting can enforce fan speed when the host, process, or serial connection is unavailable.

## Discovery profiles and validation

`sc200 discover --profile safe` uses the defaults documented here. `--profile quiet` changes only `min_speed` to `10`, `safe_speed` to `15`, and the curve to `25°C→10%`, `30→10%`, `35→12%`, `40→15%`, `41→20%`, `45→20%`. Both profiles retain the default critical threshold, which requests `100%` at `45°C`.

Profiles generate explicit YAML for new devices; there is no runtime `profile` field or profile inheritance. Existing entries retain their settings. Default temperatures and speeds are initial software settings that require hardware commissioning under real workload.

Validate edits without opening serial ports, then restart the service:

```sh
sudo -u sc200 sc200 --check-config --config /etc/sc200/config.yaml
sudo systemctl restart sc200.service
```

Use the configured service account and path if they differ. Validation checks syntax and policy constraints; it does not confirm device availability, physical fan actuation, or thermal suitability.
