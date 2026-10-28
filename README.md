# sc200

sc200 controls Dell SC200 enclosure fans over serial, adjusting speed as temperatures change. One daemon can manage several enclosures independently, with Prometheus metrics and a JSON health endpoint. It runs on Linux amd64 and arm64.

- Temperature-based fan curves, with hysteresis and a cooldown to avoid frequent speed changes.
- Configurable fallback speed when readings are missing or communication fails.
- Interactive setup with device discovery and safe, quiet, or custom settings.
- Dry run to inspect temperatures and proposed speeds before enabling control.

Console compatibility is based on firmware `12Pack.Slot0.101`; other firmware and models need verification. Fan settings also need testing on your hardware under load. If the host, daemon, or serial connection fails, sc200 cannot enforce cooling.

## Install

Download and review the [installer](https://github.com/MrZoidberg/sc200/blob/master/scripts/install.sh), then run it on the Linux host connected to your enclosure:

```sh
sudo bash scripts/install.sh
```

This command assumes you saved the installer as `scripts/install.sh`. It needs systemd, curl, GNU tar, sha256sum, and standard Linux account-management tools. Go is not required.

The installer downloads the latest release, checks its SHA-256 checksum, and installs the binary, systemd service, and example configuration. It uses the `sc200` service account and the `dialout` serial group by default. Use `--serial-group GROUP` if your devices belong to another group.

On a fresh installation, the setup wizard opens in your terminal. Select the enclosures to control, choose their fan settings, and review the configuration before saving. Starting the service is optional. Without a terminal, or when using `--no-setup`, run setup later:

```sh
sudo sc200 --setup
```

Before discovery or startup, disable any old fan-control cron job, including `quite.sh`, and close serial terminals using the same ports. The installer does not edit cron. Stop an existing sc200 service before reopening setup.

## Configure

Settings live in `/etc/sc200/config.yaml`. The wizard discovers serial devices and records their sensor indices. New devices start unselected, and canceling setup leaves the configuration unchanged.

For command-line setup, preview discovery first. Specify known ports to avoid probing other serial equipment:

```sh
sudo sc200 discover --port /dev/ttyS0
sudo sc200 discover --port /dev/ttyS0 --write
sudo sc200 --check-config
```

Discovery reads telemetry without changing fan speeds. It saves only with `--write`, preserves existing enclosure settings, and prefers stable `/dev/serial/by-id/` paths when available. Add `--verbose` to see failures on every port during an automatic scan.

The default **safe** profile starts and falls back at 100%, with a curve from 20% at 25°C to 100% at 45°C. The **quiet** profile uses lower speeds, including a 15% fallback; choose it only after checking cooling under sustained load. Both profiles request 100% at the default critical temperature of 45°C or on a fan fault, subject to valid required temperature readings. Profiles apply only to newly discovered devices:

```sh
sudo sc200 discover --port /dev/ttyS0 --profile quiet --write
```

To observe the policy before controlling fans, set top-level `dry_run: true` in the configuration. sc200 will read real telemetry and log proposed speeds while leaving fan control to the enclosure's existing settings. Set it back to `false` to enable control.

After reviewing your settings, start the service:

```sh
sudo systemctl enable --now sc200.service
```

For later edits, validate and restart:

```sh
sudo -u sc200 sc200 --check-config
sudo systemctl restart sc200.service
```

There is no automatic reload. For another configuration path, pass `--config /absolute/path` to the installer and sc200 commands. The service account needs read access to the file and traversal access to its parent directories.

See the [configuration reference](https://github.com/MrZoidberg/sc200/blob/master/docs/configuration.md) for all fields, defaults, and fan-curve behavior.

## Monitor and troubleshoot

```sh
journalctl -u sc200.service -f
curl http://127.0.0.1:9700/api/v1/health
curl http://127.0.0.1:9700/metrics
```

The HTTP server listens on `127.0.0.1:9700` by default. It has no authentication; restrict network access if you expose it remotely.

| Endpoint | Purpose |
| --- | --- |
| `/api/v1/health` | Per-enclosure status, readings, and failure reasons |
| `/readyz` | Returns 503 if any enabled enclosure is unhealthy |
| `/healthz` | Process liveness, regardless of enclosure health |
| `/metrics` | Prometheus temperatures, fan RPM, targets, freshness, and errors |

Startup and recovery normally need three valid samples before reporting healthy. If readiness stays at 503, check the health response and journal for stale sensors, device permissions, busy ports, or communication errors. One enclosure's failure does not stop the others.

Speed-change logs include the enclosure, commanded percentage, and fresh temperatures. A completed command confirms communication; use measured RPM and independent temperature checks to verify cooling.

## Grafana dashboard

The [SC200 dashboard](https://github.com/MrZoidberg/sc200/blob/master/contrib/grafana/sc200.json) shows temperatures, measured fan RPM, requested and completed speeds, health, telemetry age, and errors. Filter by instance or enclosure. Check logs or health JSON for dry-run status; simulated commands are not exported as metrics.

1. Add sc200 to your Prometheus configuration under `scrape_configs`:

   ```yaml
   scrape_configs:
     - job_name: sc200
       scrape_interval: 10s
       static_configs:
         - targets: ["127.0.0.1:9700"]
   ```

   This address works when Prometheus runs directly on the sc200 host. For a remote or containerized Prometheus, use a reachable host address, adjust sc200's `http.listen`, and restrict access to the unauthenticated endpoint. Reload or restart Prometheus and check that its sc200 target is up.

2. Add your Prometheus server as a data source in Grafana if you haven't already.
3. Download `contrib/grafana/sc200.json`. In Grafana, select **Dashboards → New → Import**, upload the JSON, and import it. Choose your Prometheus source in the dashboard's **Prometheus** selector.

Missing readings appear as gaps or **No data**, not zero. Check **Scrape status** and **Telemetry freshness** before interpreting temperatures or RPM. The dashboard does not install alert rules; see [operations](https://github.com/MrZoidberg/sc200/blob/master/docs/operations.md#http-and-metrics) for suggested alerts.

## Upgrade or stop

Run the installer again to upgrade. To choose a release, add `--version vX.Y.Z`, replacing the placeholder with a published tag. Existing configuration is preserved and validated before replacement. An active service restarts; if startup fails, the installer restores the previous binary and unit and attempts to restart the old service. Check the journal after a failed upgrade.

Stopping sc200 closes serial connections without changing fan speed. The enclosure firmware determines how long the last setting persists. Arrange independent cooling before stopping or removing the service, and never delete its lock files while a controller may be running.

See [operations](https://github.com/MrZoidberg/sc200/blob/master/docs/operations.md) for permissions, alerts, custom service settings, and troubleshooting; [hardware notes](https://github.com/MrZoidberg/sc200/blob/master/docs/hardware.md) for compatibility evidence; and [development](https://github.com/MrZoidberg/sc200/blob/master/docs/development.md) for building and releasing.
