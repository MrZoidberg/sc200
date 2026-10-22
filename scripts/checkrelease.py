#!/usr/bin/env python3
"""Validate unpublished GoReleaser output against the standalone installer contract."""
import hashlib
from pathlib import Path
import platform
import re
import struct
import subprocess
import sys
import tarfile
import tempfile


def validate(directory):
    root = Path(directory)
    archives = sorted(root.glob('sc200_*_linux_*.tar.gz'))
    if len(archives) != 2:
        raise ValueError('expected exactly two Linux archives')
    expected = {'sc200', 'LICENSE', 'README.md', 'sc200.example.yaml', 'sc200.service', 'sc200.conf'}
    versions, architectures = set(), set()
    executed = 0
    for archive in archives:
        match = re.fullmatch(r'sc200_([0-9][A-Za-z0-9.+-]*)_linux_(amd64|arm64)\.tar\.gz', archive.name)
        if not match:
            raise ValueError(f'unexpected archive name: {archive.name}')
        version, arch = match.groups()
        versions.add(version)
        architectures.add(arch)
        with tarfile.open(archive, 'r:gz') as bundle:
            members = bundle.getmembers()
            if len(members) != len(expected) or {m.name for m in members} != expected or any(not m.isfile() for m in members):
                raise ValueError(f'{archive.name}: expected six flat regular files: {sorted(expected)}')
            binary = bundle.extractfile('sc200').read()
            mode = bundle.getmember('sc200').mode
            if mode & 0o111 == 0:
                raise ValueError('binary is not executable')
            # ELF64, little-endian; assert architecture and lack of dynamic interpreter.
            if binary[:6] != b'\x7fELF\x02\x01' or struct.unpack_from('<H', binary, 18)[0] != {'amd64': 62, 'arm64': 183}[arch]:
                raise ValueError(f'wrong ELF architecture: {arch}')
            offset = struct.unpack_from('<Q', binary, 32)[0]
            size, count = struct.unpack_from('<HH', binary, 54)
            if any(struct.unpack_from('<I', binary, offset + i * size)[0] == 3 for i in range(count)):
                raise ValueError(f'{arch}: binary requires a dynamic interpreter')
            native = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine())
            if platform.system() == 'Linux' and arch == native:
                with tempfile.TemporaryDirectory() as work:
                    executable = Path(work) / 'sc200'
                    executable.write_bytes(binary)
                    executable.chmod(0o755)
                    result = subprocess.run([str(executable), '--version'], check=True, text=True, capture_output=True, timeout=10)
                    if not result.stdout.startswith(f'sc200 {version} (commit ') or 'unknown' in result.stdout:
                        raise ValueError(f'incorrect embedded metadata: {result.stdout}')
                    executed += 1
                    print(result.stdout.strip())
        print(f'{archive.name}: six assets, static {arch} ELF')
    if len(versions) != 1 or architectures != {'amd64', 'arm64'}:
        raise ValueError('archive version/architecture mismatch')
    checksum_file = root / f'sc200_{versions.pop()}_checksums.txt'
    checksums = {}
    for line in checksum_file.read_text().splitlines():
        match = re.fullmatch(r'([a-f0-9]{64})  (sc200_[^ /]+\.tar\.gz)', line)
        if not match or match[2] in checksums:
            raise ValueError('checksum file is not unique sha256sum-compatible archive entries')
        checksums[match[2]] = match[1]
    if set(checksums) != {a.name for a in archives}:
        raise ValueError('checksum manifest differs from release archive set')
    for archive in archives:
        if hashlib.sha256(archive.read_bytes()).hexdigest() != checksums[archive.name]:
            raise ValueError(f'checksum mismatch for {archive.name}')
    print('SHA-256 manifest matches installer naming and standard sha256sum format')
    if not executed:
        print('Native Linux version execution skipped on this host; Linux CI requires it')


if __name__ == '__main__':
    try:
        if len(sys.argv) != 2:
            raise ValueError('usage: checkrelease.py DIST_DIRECTORY')
        validate(sys.argv[1])
    except (ValueError, OSError, tarfile.TarError, subprocess.SubprocessError, struct.error) as error:
        sys.exit(str(error))
