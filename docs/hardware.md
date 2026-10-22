# Hardware evidence and commissioning

The committed verbatim protocol fixtures in `internal/protocol/testdata/` originate from the user's planning-session transcript on firmware `12Pack.Slot0.101`; see that directory's README for exact provenance. They preserve `_map`, `devils`/extended help, and the initial `_temp_rd` rejection. The original full private session is intentionally untracked. Only the initial rejection/first repetition is retained for `_temp_rd`: subsequent host echo feedback is not useful protocol evidence.

The captured `_map` has four temperature elements and four fans, but counts are not universal. Aggregate section rows are excluded. Type 04 temperature byte three is Celsius plus 20; zero is unavailable, not −20°C. Sample hex bytes `32`, `34`, `33`, `29` decode to 30, 32, 31, 21°C. Threshold rows are separate metadata. Status/failure/overtemperature flags determine whether a reading is usable. Type 03 cooling speed uses SES factor semantics: factor 0 is N×10 RPM, factor 1 N×10+20480, factor 2 N×100; factor 3 is invalid. `01 01 0C 20` decodes to 2680 RPM. These semantics were cross-checked against [sg3_utils sg_ses.c](https://github.com/doug-gilbert/sg3_utils/blob/main/src/sg_ses.c); it is not a runtime dependency.

The sample's second PSU status is 03 (noncritical), not 05 (not installed). Do not assume a fault is an intentionally absent slot. The implementation reports relevant faults without decoding complete electrical telemetry. Installed failed/unavailable/stopped fans trigger conservative fallback. Noninstalled elements are distinguished from faults.

The [linked command reference](https://pastebin.com/C0y5eavr) describes another firmware; `_temp_rd` is rejected on the captured device. Discovery uses compatible `devils` identity evidence plus valid `_map` structure; a generic prompt alone is insufficient, and related models may share signatures. No firmware qualification beyond this evidence is claimed.

## Capture a full read-only transcript

Use a controlled maintenance session with independently assured cooling. Stop/disable the old cron controller, stop the daemon, confirm no process/terminal owns the port, and coordinate ownership before capture. Do not interrupt the only working cooling control without a replacement procedure. Advisory locks do not prevent an unrelated terminal from accessing the same port.

On Linux, install Python 3 and use the script below with a known enclosure port. It configures raw 38400 8N1 with host echo and flow control disabled (equivalent to `stty ... raw -echo`), holds the compatibility flock, requests kernel serial exclusivity, sends only CR-terminated read commands and records all received bytes. It does not set fan speed. A competing daemon may open a different port during this manual session unless operationally stopped. Device-specific controller locks remain another reason to stop all owners first.

```sh
# Save the following Python as capture-sc200.py, then:
sudo python3 capture-sc200.py /dev/ttyS0 > sc200-readonly-transcript.bin
```

```python
import fcntl
import os
import select
import sys
import termios
import time
import tty

with open('/run/sc200_fan_control.lock', 'a') as lock:
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    fd = os.open(sys.argv[1], os.O_RDWR | os.O_NOCTTY | os.O_NONBLOCK)
    original = termios.tcgetattr(fd)
    try:
        fcntl.ioctl(fd, termios.TIOCEXCL)
        tty.setraw(fd, termios.TCSANOW)
        attrs = termios.tcgetattr(fd)
        attrs[0] &= ~(termios.IXON | termios.IXOFF | termios.IXANY)
        attrs[2] &= ~(termios.CSIZE | termios.PARENB | termios.CSTOPB | termios.CRTSCTS)
        attrs[2] |= termios.CS8 | termios.CREAD | termios.CLOCAL
        attrs[3] &= ~(termios.ECHO | termios.ECHONL | termios.ICANON)
        attrs[4] = attrs[5] = termios.B38400
        termios.tcsetattr(fd, termios.TCSANOW, attrs)
        def receive(seconds):
            end = time.monotonic() + seconds
            while time.monotonic() < end:
                ready, _, _ = select.select([fd], [], [], max(0, end-time.monotonic()))
                if ready:
                    data = os.read(fd, 65536)
                    if not data:
                        raise RuntimeError('serial disconnected')
                    sys.stdout.buffer.write(data)
                    sys.stdout.buffer.flush()
        receive(2)
        for command in (b'\r', b'devils\r', b'_devils\r', b'_map\r'):
            os.write(fd, command)
            receive(5)
    finally:
        termios.tcsetattr(fd, termios.TCSANOW, original)
        os.close(fd)
```

Preserve the original bytes; make a separate annotated copy with port, baud, firmware, date, populated layout and command sequence. Inspect for complete start/end prompts and no truncation. Increase the bounded receive window and repeat a read-only command if necessary. Never reinterpret a partial capture as a complete sample. Do not repeatedly issue an unsupported command or enable console echo. The existing `_temp_rd` fixture is sufficient evidence of rejection; reissuing it is unnecessary. Review transcripts for sensitive identifiers before sharing.

## Physical control limits

`set_speed N` appears in captured help, but its real response and override persistence remain uncaptured. A prompt with no firmware error means only that a transaction completed. Target percentage, completed-command percentage and observed RPM are distinct; RPM does not establish an exact percentage.

Manual commissioning must capture `set_speed` output by reasserting the already intended operating percentage in a controlled session, observe RPM before/after and over time, and compare decoded telemetry with independent SES readings. Do not run cron and daemon concurrently. Validate every attached firmware, populated fan/PSU layout, reconnect/reboot, sensor calibration and sustained workload before lowering the conservative curve.

Startup/reconnect requests safe speed before lower settings. SIGTERM/SIGINT attempts 100% concurrently on reachable enclosures within the configured shared deadline. Software cannot guarantee that hardware applied it. Test normal stop and restoration with independent observation. Cable removal, SIGKILL, power/host failure and firmware override expiry are outside software enforcement; do not assume firmware returns to automatic control. Confirm the actual behavior under an approved maintenance procedure with independent cooling available. These hardware checks have not been performed by the unit/component test suite.
