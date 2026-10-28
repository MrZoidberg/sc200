#!/usr/bin/env bash
# Standalone: no checkout, Go, git or jq required. Only verified GitHub assets.
set -euo pipefail
umask 077
version=latest
account=sc200
serial_group=dialout
config=/etc/sc200/config.yaml
start=false
setup=true
usage() {
  cat <<'HELP'
Usage: install.sh [--version vX.Y.Z] [--user NAME] [--serial-group GROUP]
                  [--config /absolute/path] [--start] [--no-setup]
Linux amd64/arm64; run as root. Defaults: latest GitHub release from
MrZoidberg/sc200, user sc200, serial group dialout, /etc/sc200/config.yaml.
Downloads require curl, GNU tar, sha256sum and standard Linux account/systemd tools.
Checksums are fetched from the same HTTPS release (integrity, not a signature).
Preserves active configuration; installs a disabled example as config.example.yaml
beside it. When control is stopped, launch sc200 --setup on the controlling terminal.
The wizard discovers devices, configures control and optionally starts the service.
Without a terminal (or with --no-setup), run sudo sc200 --setup later.
--start validates real configuration before enabling/starting. An active service
is restarted after a validated upgrade; failed restart restores its old binary.
--config installs an explicit systemd ExecStart/ExecStartPre drop-in for that path;
use the same --config path with discovery. Existing config files are never replaced.
New config directories inherit the service group; in an existing custom directory,
ensure the generated config is readable by the service user before starting.
No cron files are edited: disable the old controller before starting this service.
SC200_INSTALL_ROOT is reserved for isolated component-test filesystem staging.
HELP
}
fail() { printf 'install: %s\n' "$*" >&2; exit 1; }
while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --version|--user|--serial-group|--config)
      (($# >= 2)) || fail "missing value for $1"
      case "$1" in
        --version) version=$2 ;; --user) account=$2 ;;
        --serial-group) serial_group=$2 ;; --config) config=$2 ;;
      esac
      shift 2 ;;
    --start) start=true; shift ;;
    --no-setup) setup=false; shift ;;
    *) fail "unknown option: $1" ;;
  esac
done
[[ $version == latest || $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9][a-zA-Z0-9.-]*)?$ ]] || fail 'unsafe release version'
[[ $account =~ ^[a-z_][a-z0-9_-]*$ && $serial_group =~ ^[a-z_][a-z0-9_-]*$ ]] || fail 'unsafe account/group name'
[[ $config =~ ^/[a-zA-Z0-9_./-]+$ && $config != *'/../'* && $config != */.. ]] || fail 'config requires a simple absolute path without parent traversal'
for tool in id uname curl tar sha256sum mktemp install mv cp rm mkdir sed awk sort uniq getent useradd usermod groupadd systemctl systemd-tmpfiles runuser dirname chmod; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done
[[ $(id -u) == 0 ]] || fail 'must run as root'
[[ $(uname -s) == Linux ]] || fail 'only Linux is supported'
case $(uname -m) in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) fail 'unsupported architecture' ;; esac
root=${SC200_INSTALL_ROOT:-}
[[ -z $root || $root == /* ]] || fail 'staging root must be absolute'
bin=$root/usr/local/bin/sc200
unit=$root/etc/systemd/system/sc200.service
conf=$root$config
work=$(mktemp -d)
staged_bin=
cleanup() { rm -rf -- "$work"; if [[ -n $staged_bin ]]; then rm -f -- "$staged_bin"; fi; }
trap cleanup EXIT
fetch() {
  curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --connect-timeout 10 --max-time 120 --max-filesize 52428800 --retry 2 --output "$2" "$1"
}
base=https://github.com/MrZoidberg/sc200/releases
if [[ $version == latest ]]; then
  # GitHub's redirect resolves the tag without parsing JSON or requiring jq.
  url=$(curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --connect-timeout 10 --max-time 120 --output /dev/null --write-out '%{url_effective}' "$base/latest")
  [[ $url == "$base/tag/"* ]] || fail 'unexpected latest-release redirect'
  version=${url##*/}
  [[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9][a-zA-Z0-9.-]*)?$ ]] || fail 'unsafe latest release version'
fi
release=${version#v}
asset=sc200_${release}_linux_${arch}.tar.gz
fetch "$base/download/$version/$asset" "$work/$asset"
fetch "$base/download/$version/sc200_${release}_checksums.txt" "$work/checksums"
# Require exactly one well-formed entry; never trust paths from the checksum file.
hash=$(awk -v asset="$asset" '$2 == asset || $2 == "*" asset {print $1}' "$work/checksums")
[[ $hash =~ ^[a-fA-F0-9]{64}$ ]] || fail 'missing, duplicate or invalid archive checksum'
printf '%s  %s\n' "$hash" "$asset" > "$work/verify"
(cd "$work" && sha256sum --check verify) || fail 'archive checksum mismatch'
tar -tzf "$work/$asset" > "$work/members"
# Flat release archives only; reject links, directories, traversal and duplicates.
while IFS= read -r member; do
  case "$member" in sc200|LICENSE|sc200.example.yaml|sc200.service|sc200.conf|README.md) ;; *) fail "unsafe archive member: $member" ;; esac
done < "$work/members"
[[ -z $(sort "$work/members" | uniq -d) ]] || fail 'duplicate archive members'
tar -tvzf "$work/$asset" > "$work/types"
awk 'substr($0,1,1) != "-" {exit 1}' "$work/types" || fail 'archive members must be regular files'
mkdir "$work/unpacked"
tar -xzf "$work/$asset" --no-same-owner --no-same-permissions -C "$work/unpacked"
for member in sc200 LICENSE sc200.example.yaml sc200.service sc200.conf README.md; do
  [[ -f $work/unpacked/$member && ! -L $work/unpacked/$member ]] || fail "missing archive member: $member"
done
chmod 0755 "$work/unpacked/sc200"
active=false
if systemctl is-active --quiet sc200.service; then active=true; fi
if [[ -e $conf ]]; then
  "$work/unpacked/sc200" --check-config --config "$conf" >/dev/null || fail 'existing configuration rejected; installed files unchanged'
elif $start || $active; then
  fail "configuration $config is absent; install without --start, then discover --write"
fi
getent group "$serial_group" >/dev/null || fail "serial group $serial_group does not exist; create it and grant device access first"
getent group "$account" >/dev/null || groupadd --system "$account"
if ! id "$account" >/dev/null 2>&1; then
  useradd --system --gid "$account" --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "$account"
fi
usermod --append --groups "$serial_group" "$account"
# Check readability as the actual service identity before touching installed files.
if [[ -e $conf ]]; then
  chmod 0755 "$work" "$work/unpacked"
  runuser -u "$account" -- "$work/unpacked/sc200" --check-config --config "$conf" >/dev/null || fail 'service account cannot validate configuration'
fi
install -d -m 0755 "$root/usr/local/bin" "$root/etc/systemd/system" "$root/etc/tmpfiles.d"
if [[ ! -d $(dirname "$conf") ]]; then
  install -d -m 2750 -o root -g "$account" "$(dirname "$conf")"
fi
if [[ ! -e $(dirname "$conf")/config.example.yaml ]]; then
  install -m 0640 -o root -g "$account" "$work/unpacked/sc200.example.yaml" "$(dirname "$conf")/config.example.yaml"
fi
sed -e "s/^User=sc200$/User=$account/" -e "s/^Group=sc200$/Group=$account/" \
    -e "s/^SupplementaryGroups=dialout$/SupplementaryGroups=$serial_group/" "$work/unpacked/sc200.service" > "$work/unit"
sed "s/0660 sc200 sc200/0660 $account $account/" "$work/unpacked/sc200.conf" > "$work/tmpfiles"
# Preserve rollback data, including the unit and custom-path override.
had_bin=false; had_unit=false; had_drop=false; had_tmpfiles=false
tmpfiles=$root/etc/tmpfiles.d/sc200.conf
if [[ -e $tmpfiles ]]; then cp -p "$tmpfiles" "$work/old-tmpfiles"; had_tmpfiles=true; fi
if [[ -e $bin ]]; then cp -p "$bin" "$work/old-bin"; had_bin=true; fi
if [[ -e $unit ]]; then cp -p "$unit" "$work/old-unit"; had_unit=true; fi
drop=$root/etc/systemd/system/sc200.service.d/10-config.conf
if [[ -e $drop ]]; then cp -p "$drop" "$work/old-drop"; had_drop=true; fi
install -m 0644 "$work/unit" "$unit"
install -m 0644 "$work/tmpfiles" "$root/etc/tmpfiles.d/sc200.conf"
systemd-tmpfiles --create "$root/etc/tmpfiles.d/sc200.conf"
if [[ $config != /etc/sc200/config.yaml ]]; then
  install -d -m 0755 "$(dirname "$drop")"
  printf '[Service]\nExecStartPre=\nExecStartPre=/usr/local/bin/sc200 --check-config --config %s\nExecStart=\nExecStart=/usr/local/bin/sc200 --config %s\n' "$config" "$config" > "$work/drop"
  install -m 0644 "$work/drop" "$drop"
elif [[ -e $drop ]]; then
  rm -f "$drop"
fi
staged_bin=$(mktemp "$root/usr/local/bin/.sc200.XXXXXX")
install -m 0755 "$work/unpacked/sc200" "$staged_bin"
mv -f "$staged_bin" "$bin"
staged_bin=
systemctl daemon-reload
rollback() {
  if $had_bin; then
    staged_bin=$(mktemp "$root/usr/local/bin/.sc200.XXXXXX")
    install -m 0755 "$work/old-bin" "$staged_bin"
    mv -f "$staged_bin" "$bin"; staged_bin=
  else rm -f "$bin"; fi
  if $had_unit; then install -m 0644 "$work/old-unit" "$unit"; else rm -f "$unit"; fi
  if $had_drop; then install -m 0644 "$work/old-drop" "$drop"; else rm -f "$drop"; fi
  if $had_tmpfiles; then
    install -m 0644 "$work/old-tmpfiles" "$tmpfiles"
    systemd-tmpfiles --create "$tmpfiles"
  else rm -f "$tmpfiles"; fi
  systemctl daemon-reload
  if $active; then systemctl restart sc200.service || printf 'install: old service restart also failed; inspect journald immediately\n' >&2; fi
}
if $active; then
  if ! systemctl restart sc200.service; then rollback; fail 'restart failed; previous binary/unit restored'; fi
elif $start; then
  if ! systemctl start sc200.service; then rollback; fail 'start failed; previous binary/unit restored'; fi
  systemctl enable sc200.service
fi
printf 'Installed sc200 %s. Configuration preserved at %s.\n' "$version" "$config"
if ! $active && ! $start; then
  if $setup && [[ -z $root ]] && { exec 3<>/dev/tty; } 2>/dev/null; then
    "$bin" --setup --config "$conf" <&3 >&3 2>&3 || fail "setup did not complete; run sudo sc200 --setup --config $config to retry"
    exec 3>&-
  else
    printf 'Control is stopped. Run sudo sc200 --setup --config %s to configure devices and optionally start the service.\n' "$config"
  fi
fi
