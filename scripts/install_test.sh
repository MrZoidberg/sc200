#!/usr/bin/env bash
# Component tests: all privileged/network/service commands are stubs.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
suite=$(mktemp -d)
trap 'rm -rf "$suite"' EXIT
real_install=$(command -v install)
real_id=$(command -v id)
export REAL_INSTALL=$real_install REAL_ID=$real_id
mkdir -p "$suite/stubs" "$suite/payload"
cat > "$suite/stubs/uname" <<'STUB'
#!/usr/bin/env bash
case "$1" in -s) echo "${TEST_OS:-Linux}" ;; -m) echo "${TEST_ARCH:-x86_64}" ;; esac
STUB
cat > "$suite/stubs/id" <<'STUB'
#!/usr/bin/env bash
if [[ $1 == -u ]]; then echo "${TEST_UID:-0}"; else [[ ${TEST_NEW_ACCOUNT:-0} == 0 ]]; fi
STUB
cat > "$suite/stubs/curl" <<'STUB'
#!/usr/bin/env bash
set -eu
[[ ${TEST_DOWNLOAD_FAIL:-0} == 0 ]] || exit 22
out=; url=${!#}
while (($#)); do if [[ $1 == --output ]]; then out=$2; shift; fi; shift; done
if [[ $url == */latest ]]; then echo 'https://github.com/MrZoidberg/sc200/releases/tag/v1.2.3'; exit; fi
case "$url" in
  *.tar.gz) cp "$TEST_ARCHIVE" "$out" ;;
  *_checksums.txt) cp "$TEST_CHECKSUMS" "$out" ;;
  *) exit 1 ;;
esac
STUB
cat > "$suite/stubs/install" <<'STUB'
#!/usr/bin/env bash
args=()
while (($#)); do case "$1" in -o|-g) shift 2 ;; *) args+=("$1"); shift ;; esac; done
exec "$REAL_INSTALL" "${args[@]}"
STUB
cat > "$suite/stubs/systemctl" <<'STUB'
#!/usr/bin/env bash
printf 'systemctl %s\n' "$*" >> "$TEST_LOG"
if [[ $1 == is-active ]]; then [[ ${TEST_ACTIVE:-0} == 1 ]]; exit; fi
if [[ $1 == restart || $1 == start ]]; then
  if [[ ${TEST_RESTART_FAIL:-0} == 1 && ! -e $TEST_ROOT/failed ]]; then touch "$TEST_ROOT/failed"; exit 1; fi
fi
STUB
cat > "$suite/stubs/runuser" <<'STUB'
#!/usr/bin/env bash
printf 'runuser %s\n' "$*" >> "$TEST_LOG"
[[ ${TEST_ACCOUNT_FAIL:-0} == 0 ]] || exit 1
shift 3
exec "$@"
STUB
for command in getent useradd usermod groupadd systemd-tmpfiles; do
  cat > "$suite/stubs/$command" <<'STUB'
#!/usr/bin/env bash
printf '%s %s\n' "${0##*/}" "$*" >> "$TEST_LOG"
if [[ ${0##*/} == getent ]]; then
  [[ ${TEST_MISSING_GROUP:-0} == 0 ]] || exit 1
  if [[ $2 == sc200 && ${TEST_NEW_ACCOUNT:-0} == 1 ]]; then exit 1; fi
fi
STUB
done
chmod +x "$suite/stubs/"*
cat > "$suite/payload/sc200" <<'STUB'
#!/usr/bin/env bash
printf 'validate %s\n' "$*" >> "$TEST_LOG"
[[ ${TEST_VALIDATE_FAIL:-0} == 0 ]]
STUB
chmod +x "$suite/payload/sc200"
cp "$repo/packaging/systemd/sc200.service" "$repo/packaging/systemd/sc200.conf" "$repo/LICENSE" "$repo/README.md" "$suite/payload/"
cp "$repo/configs/sc200.example.yaml" "$suite/payload/sc200.example.yaml"
tar -czf "$suite/good.tar.gz" -C "$suite/payload" sc200 sc200.service sc200.conf sc200.example.yaml LICENSE README.md
export PATH="$suite/stubs:$PATH"
count=0
new_case() {
  count=$((count + 1))
  export TEST_ROOT="$suite/case-$count" SC200_INSTALL_ROOT="$suite/case-$count/root"
  mkdir -p "$TEST_ROOT/tmp" "$SC200_INSTALL_ROOT"
  export TMPDIR="$TEST_ROOT/tmp" TEST_LOG="$TEST_ROOT/log"
  : > "$TEST_LOG"
  unset TEST_OS TEST_ARCH TEST_UID TEST_DOWNLOAD_FAIL TEST_ACTIVE TEST_RESTART_FAIL TEST_VALIDATE_FAIL TEST_ACCOUNT_FAIL TEST_NEW_ACCOUNT TEST_MISSING_GROUP
  export TEST_ARCHIVE="$suite/good.tar.gz" TEST_CHECKSUMS="$TEST_ROOT/checksums"
  checksum
}
checksum() {
  local digest
  digest=$(sha256sum "$TEST_ARCHIVE"); digest=${digest%% *}
  printf '%s  sc200_1.2.3_linux_amd64.tar.gz\n%s  sc200_1.2.3_linux_arm64.tar.gz\n' "$digest" "$digest" > "$TEST_CHECKSUMS"
}
run_ok() {
  if ! bash "$repo/scripts/install.sh" "$@" > "$TEST_ROOT/output" 2>&1; then cat "$TEST_ROOT/output"; exit 1; fi
  [[ -z $(ls -A "$TMPDIR") ]] || { echo 'temporary files leaked'; exit 1; }
}
run_fail() {
  if bash "$repo/scripts/install.sh" "$@" > "$TEST_ROOT/output" 2>&1; then echo "unexpected success $*"; exit 1; fi
  [[ -z $(ls -A "$TMPDIR") ]] || { echo 'temporary files leaked'; exit 1; }
}
existing() {
  mkdir -p "$SC200_INSTALL_ROOT/usr/local/bin" "$SC200_INSTALL_ROOT/etc/sc200"
  printf 'old binary\n' > "$SC200_INSTALL_ROOT/usr/local/bin/sc200"
  printf '# preserved configuration\n' > "$SC200_INSTALL_ROOT/etc/sc200/config.yaml"
}
for arch in x86_64 aarch64 amd64 arm64; do
  new_case; export TEST_ARCH=$arch; run_ok --version v1.2.3
  [[ -x $SC200_INSTALL_ROOT/usr/local/bin/sc200 ]]
  [[ -e $SC200_INSTALL_ROOT/etc/sc200/config.example.yaml && ! -e $SC200_INSTALL_ROOT/etc/sc200/config.yaml ]]
  if grep -Eq 'systemctl (start|restart|enable)' "$TEST_LOG"; then exit 1; fi
done
new_case; export TEST_NEW_ACCOUNT=1; run_ok --version v1.2.3
grep -q "useradd --system" "$TEST_LOG"
grep -q "groupadd --system" "$TEST_LOG"
new_case; export TEST_MISSING_GROUP=1; run_fail --version v1.2.3
new_case; run_ok # latest redirect
grep -q "sc200 --setup" "$TEST_ROOT/output"
new_case; run_ok --no-setup --version v1.2.3
grep -q "sc200 --setup" "$TEST_ROOT/output"
new_case; existing; export TEST_ACTIVE=1; run_ok --version v1.2.3
[[ $(cat "$SC200_INSTALL_ROOT/etc/sc200/config.yaml") == '# preserved configuration' ]]
awk '/validate/ {v=NR} /systemctl restart/ {if (!v || v>=NR) exit 1; found=1} END {if (!found) exit 1}' "$TEST_LOG"
new_case; existing; run_ok --version v1.2.3 --start
awk '/systemctl start/ {s=NR} /systemctl enable/ {if (!s || s>=NR) exit 1; found=1} END {if (!found) exit 1}' "$TEST_LOG"
new_case; run_fail --start --version v1.2.3
[[ ! -e $SC200_INSTALL_ROOT/usr/local/bin/sc200 ]]
new_case; export TEST_OS=Darwin; run_fail
new_case; export TEST_ARCH=ppc64; run_fail
new_case; export TEST_UID=1000; run_fail
new_case; run_fail --version '../../oops'
new_case; run_fail --config '/etc/a b'
new_case; run_fail --user 'bad;name'
new_case; run_fail --unknown
new_case; run_fail --version
new_case; export TEST_DOWNLOAD_FAIL=1; run_fail --version v1.2.3
new_case; printf '%064d  sc200_1.2.3_linux_amd64.tar.gz\n' 0 > "$TEST_CHECKSUMS"; run_fail --version v1.2.3
new_case; cat "$TEST_CHECKSUMS" >> "$TEST_ROOT/duplicate"; cat "$TEST_ROOT/duplicate" >> "$TEST_CHECKSUMS"; run_fail --version v1.2.3
new_case; existing; export TEST_VALIDATE_FAIL=1 TEST_ACTIVE=1; run_fail --version v1.2.3
[[ $(cat "$SC200_INSTALL_ROOT/usr/local/bin/sc200") == 'old binary' ]]
if grep -q 'systemctl restart' "$TEST_LOG"; then exit 1; fi
new_case; existing; export TEST_ACCOUNT_FAIL=1; run_fail --version v1.2.3
[[ $(cat "$SC200_INSTALL_ROOT/usr/local/bin/sc200") == 'old binary' ]]
new_case; existing; export TEST_ACTIVE=1 TEST_RESTART_FAIL=1; run_fail --version v1.2.3
[[ $(cat "$SC200_INSTALL_ROOT/usr/local/bin/sc200") == 'old binary' ]]
[[ $(grep -c 'systemctl restart' "$TEST_LOG") == 2 ]]
new_case; existing; export TEST_RESTART_FAIL=1; run_fail --version v1.2.3 --start
[[ $(cat "$SC200_INSTALL_ROOT/usr/local/bin/sc200") == 'old binary' ]]
if grep -q 'systemctl enable' "$TEST_LOG"; then exit 1; fi
new_case; run_ok --version v1.2.3 --user enclosure --serial-group serial --config /etc/enclosure/active.yaml
grep -q '^User=enclosure$' "$SC200_INSTALL_ROOT/etc/systemd/system/sc200.service"
grep -q 'config /etc/enclosure/active.yaml' "$SC200_INSTALL_ROOT/etc/systemd/system/sc200.service.d/10-config.conf"
grep -q '0660 enclosure enclosure' "$SC200_INSTALL_ROOT/etc/tmpfiles.d/sc200.conf"
# Reject symbolic links, unknown/traversal paths, duplicate and missing members.
new_case
ln -s /etc/passwd "$suite/payload/evil"
tar -czf "$TEST_ROOT/unsafe.tar.gz" -C "$suite/payload" evil
export TEST_ARCHIVE="$TEST_ROOT/unsafe.tar.gz"; checksum; run_fail --version v1.2.3
new_case
existing
 tar -czf "$TEST_ROOT/unsafe.tar.gz" -C "$suite/payload" sc200 sc200.service sc200.conf sc200.example.yaml LICENSE README.md sc200
export TEST_ARCHIVE="$TEST_ROOT/unsafe.tar.gz"; checksum; run_fail --version v1.2.3
[[ $(cat "$SC200_INSTALL_ROOT/usr/local/bin/sc200") == 'old binary' ]]
[[ $(cat "$SC200_INSTALL_ROOT/etc/sc200/config.yaml") == '# preserved configuration' ]]
[[ ! -e $SC200_INSTALL_ROOT/etc/systemd/system/sc200.service ]]
new_case
tar -czf "$TEST_ROOT/unsafe.tar.gz" -C "$suite/payload" LICENSE
export TEST_ARCHIVE="$TEST_ROOT/unsafe.tar.gz"; checksum; run_fail --version v1.2.3
new_case
# A symlink with an otherwise allowed name must also fail.
mkdir "$TEST_ROOT/links"; ln -s /etc/passwd "$TEST_ROOT/links/sc200"
tar -czf "$TEST_ROOT/unsafe.tar.gz" -C "$TEST_ROOT/links" sc200
export TEST_ARCHIVE="$TEST_ROOT/unsafe.tar.gz"; checksum; run_fail --version v1.2.3
new_case
tar -czf "$TEST_ROOT/unsafe.tar.gz" --transform='s|^sc200$|../sc200|' -C "$suite/payload" sc200
export TEST_ARCHIVE="$TEST_ROOT/unsafe.tar.gz"; checksum; run_fail --version v1.2.3
new_case
printf 'not gzip' > "$TEST_ROOT/unsafe.tar.gz"
export TEST_ARCHIVE="$TEST_ROOT/unsafe.tar.gz"; checksum; run_fail --version v1.2.3
# Missing-tool check with a minimal PATH, before any privileged mutation.
new_case
mkdir "$TEST_ROOT/minimal"; ln -s /bin/bash "$TEST_ROOT/minimal/bash"
if PATH="$TEST_ROOT/minimal" /bin/bash "$repo/scripts/install.sh" --version v1.2.3 > "$TEST_ROOT/output" 2>&1; then exit 1; fi
grep -q 'missing required tool' "$TEST_ROOT/output"
printf 'Installer component checks passed (%s isolated cases).\n' "$count"
