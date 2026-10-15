#!/usr/bin/env python3
"""Bounded, offline VZ experiment. Not the Helmr Worker or a sandbox for host code."""
from __future__ import annotations

import argparse
import ctypes
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import plistlib
import re
import selectors
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time

MIB = 1024 * 1024
GIB = 1024 * MIB
IMAGE_LIMIT = 2304 * MIB  # 2 GiB filesystem plus image/partition metadata allowance
INPUT_LIMIT = 128 * MIB
OUTPUT_LIMIT = MIB
HOST_DISK_RESERVE = 20 * GIB
HOST_FREE_MEMORY = 2 * GIB
ROOT = Path('/private/tmp') / f'helmr-macos-{os.getuid()}'
HERE = Path(__file__).resolve().parent


class SafetyError(RuntimeError):
    pass


def elapsed_clock():
    """macOS continuous time includes host sleep; wall-clock changes don't extend a run."""
    if sys.platform != 'darwin':
        return time.monotonic()
    class Timebase(ctypes.Structure):
        _fields_ = [('numer', ctypes.c_uint32), ('denom', ctypes.c_uint32)]
    lib = ctypes.CDLL('/usr/lib/libSystem.B.dylib')
    lib.mach_continuous_time.restype = ctypes.c_uint64
    base = Timebase()
    if lib.mach_timebase_info(ctypes.byref(base)) != 0 or not base.denom:
        raise SafetyError('cannot read continuous host clock')
    return lib.mach_continuous_time() * base.numer / base.denom / 1e9


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def secure_directory(path):
    try:
        path.mkdir(mode=0o700)
    except FileExistsError:
        pass
    s = path.lstat()
    if not stat.S_ISDIR(s.st_mode) or s.st_uid != os.getuid() or stat.S_IMODE(s.st_mode) != 0o700:
        raise SafetyError(f'not an owner-only directory: {path}')


def private_file(path, flags=os.O_WRONLY | os.O_CREAT | os.O_EXCL):
    fd = os.open(path, flags | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    s = os.fstat(fd)
    if not stat.S_ISREG(s.st_mode) or s.st_uid != os.getuid() or s.st_nlink != 1 or stat.S_IMODE(s.st_mode) != 0o600:
        os.close(fd)
        raise SafetyError(f'not an owned private regular file: {path}')
    return fd


def write_record(path, value):
    data = (json.dumps(value, sort_keys=True) + '\n').encode()
    if len(data) > 4096:
        raise SafetyError('record exceeds 4096-byte bound')
    with os.fdopen(private_file(path), 'wb') as f:
        f.write(data)
        f.flush()
        os.fsync(f.fileno())
    sync_directory(path.parent)


class Admission:
    """Stable inode lock plus crash-persistent admission barrier. No stale PID recovery."""
    def __init__(self, root=ROOT):
        self.root = root
        self.fd = None
        self.run = None

    def __enter__(self):
        secure_directory(self.root)
        self.fd = private_file(self.root / 'lock', os.O_RDWR | os.O_CREAT)
        try:
            fcntl.flock(self.fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            if os.path.lexists(self.root / 'active.json'):
                raise SafetyError('unresolved previous run; admission blocked (no automatic cleanup)')
            # An unrecorded directory can remain after a crash before marker publication.
            # Fail closed instead of accumulating unbounded abandoned preparations.
            if any(p.name not in ('lock',) for p in self.root.iterdir()):
                raise SafetyError('previous artifacts remain; inspect before another run')
            self.run = Path(tempfile.mkdtemp(prefix='run-', dir=self.root))
            write_record(self.root / 'active.json', {'version': 1, 'run': self.run.name, 'state': 'unresolved'})
            return self
        except BaseException:
            os.close(self.fd)
            self.fd = None
            raise

    def complete(self):
        # Only the exclusive newly-created directory is eligible, never a user path.
        if self.run is None or self.run.parent != self.root or self.run.is_symlink():
            raise SafetyError('cleanup ownership is uncertain')
        if not shutil.rmtree.avoids_symlink_attacks:
            raise SafetyError('fd-based cleanup unavailable')
        shutil.rmtree(self.run)
        (self.root / 'active.json').unlink()
        sync_directory(self.root)

    def __exit__(self, *_):
        if self.fd is not None:
            os.close(self.fd)  # Do not unlink the lock inode or unlock inherited child fd.


def copy_pinned(source, destination, expected_hash):
    if not re.fullmatch('[0-9a-f]{64}', expected_hash):
        raise SafetyError('provide the exact lower-case SHA-256 of each boot input')
    # Refuse symlink components, device files and FIFOs before any read.
    source = Path(os.path.abspath(source))
    for part in (source, *source.parents):
        if part.is_symlink():
            raise SafetyError('boot input contains a symlink')
    fd = os.open(source, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as src:
        s = os.fstat(src.fileno())
        if not stat.S_ISREG(s.st_mode) or not 0 < s.st_size <= INPUT_LIMIT:
            raise SafetyError('boot input must be a regular file of 1..128 MiB')
        digest = hashlib.sha256()
        count = 0
        with os.fdopen(private_file(destination), 'wb') as dst:
            while chunk := src.read(MIB):
                count += len(chunk)
                if count > INPUT_LIMIT:
                    raise SafetyError('boot input grew beyond 128 MiB')
                digest.update(chunk)
                dst.write(chunk)
            dst.flush()
            os.fsync(dst.fileno())
        if count != s.st_size or digest.hexdigest() != expected_hash:
            raise SafetyError('copied boot input size/hash mismatch; refusing launch')


def stop_child(child):
    # Popen retains ownership of an unreaped child. Never use saved PIDs/killall.
    if child.poll() is None:
        child.terminate()
        try:
            child.wait(timeout=3)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait(timeout=3)


def bounded_command(command, *, seconds=30, pass_fds=(), cwd=None, tick=None):
    deadline = elapsed_clock() + seconds
    child = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                             stderr=subprocess.STDOUT, cwd=cwd, pass_fds=pass_fds,
                             env={'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'HOME': str(cwd or '/private/tmp'),
                                  'TMPDIR': str(cwd or '/private/tmp'), 'LANG': 'C'})
    output = bytearray()
    selector = selectors.DefaultSelector()
    selector.register(child.stdout, selectors.EVENT_READ)
    try:
        while selector.get_map():
            if elapsed_clock() >= deadline:
                raise SafetyError('command deadline exceeded; run remains quarantined')
            if tick:
                tick()
            for key, _ in selector.select(timeout=0.2):
                data = os.read(key.fileobj.fileno(), 65536)
                if not data:
                    selector.unregister(key.fileobj)
                else:
                    output.extend(data)
                    if len(output) > OUTPUT_LIMIT:
                        raise SafetyError('command output exceeded 1 MiB; run remains quarantined')
        child.wait(timeout=max(0.01, deadline - elapsed_clock()))
        if child.returncode != 0:
            raise SafetyError(f'command failed ({child.returncode}): {bytes(output[-2048:]).decode(errors="replace")}')
        return bytes(output)
    finally:
        selector.close()
        stop_child(child)
        child.stdin.close()
        child.stdout.close()


def memory_available(vm_output):
    page = re.search(r'page size of (\d+) bytes', vm_output)
    values = dict(re.findall(r'^(Pages free|Pages speculative):\s+(\d+)\.', vm_output, re.M))
    if not page or len(values) != 2:
        raise SafetyError('unrecognized vm_stat output')
    return int(page[1]) * sum(int(v) for v in values.values())


def host_headroom(root, disk_reserve=HOST_DISK_RESERVE):
    if shutil.disk_usage(root).free < disk_reserve:
        raise SafetyError('host disk headroom below safety reserve')
    pressure = bounded_command(['/usr/sbin/sysctl', '-n', 'kern.memorystatus_vm_pressure_level'], seconds=3).strip()
    if pressure != b'1':
        raise SafetyError('host memory pressure is not normal')
    available = memory_available(bounded_command(['/usr/bin/vm_stat'], seconds=3).decode())
    if available < HOST_FREE_MEMORY:
        raise SafetyError('host free/speculative memory below 2 GiB reserve')
    return {'free_memory_bytes': available, 'free_disk_bytes': shutil.disk_usage(root).free}


class Image:
    def __init__(self, run, command):
        self.run = run
        self.path = run / 'storage.dmg'
        self.mount = run / 'volume'
        self.device = None
        self.command = command

    def create_attach(self):
        self.mount.mkdir(mode=0o700)
        self.command(['/usr/bin/hdiutil', 'create', '-size', '2g', '-fs', 'APFS',
                      '-volname', 'helmr-probe', '-type', 'UDIF', '-nospotlight', str(self.path)], seconds=60)
        if self.path.stat().st_size > IMAGE_LIMIT:
            raise SafetyError('image exceeded allocation allowance')
        result = plistlib.loads(self.command(['/usr/bin/hdiutil', 'attach', '-plist', '-nobrowse',
                    '-noautoopen', '-owners', 'on', '-mountpoint', str(self.mount), str(self.path)], seconds=30))
        entities = result.get('system-entities', [])
        if not any(e.get('mount-point') == str(self.mount) for e in entities):
            raise SafetyError('attachment did not return the owned mountpoint')
        roots = [e['dev-entry'] for e in entities if re.fullmatch(r'/dev/disk\d+', e.get('dev-entry', ''))]
        if not roots:
            raise SafetyError('attachment identity missing')
        self.device = roots[0]
        write_record(self.run / 'attachment.json', {'image': self.path.name, 'device': self.device})
        self.verify()
        if not os.path.ismount(self.mount) or shutil.disk_usage(self.mount).total > IMAGE_LIMIT:
            raise SafetyError('mounted storage is not a bounded owned volume')

    def verify(self):
        info = plistlib.loads(self.command(['/usr/bin/hdiutil', 'info', '-plist'], seconds=5))
        matching = [i for i in info.get('images', []) if i.get('image-path') == str(self.path)]
        if len(matching) != 1:
            raise SafetyError('cannot establish exact owned image identity')
        entities = matching[0].get('system-entities', [])
        if (not any(e.get('dev-entry') == self.device for e in entities)
                or not any(e.get('mount-point') == str(self.mount) for e in entities)):
            raise SafetyError('image device/mount identity changed; refusing detach')

    def detach(self):
        self.verify()
        self.command(['/usr/bin/hdiutil', 'detach', str(self.mount)], seconds=15)  # never -force
        info = plistlib.loads(self.command(['/usr/bin/hdiutil', 'info', '-plist'], seconds=5))
        if os.path.ismount(self.mount) or any(i.get('image-path') == str(self.path) for i in info.get('images', [])):
            raise SafetyError('owned image remains attached; quarantine retained')


def run_probe(args):
    if sys.platform != 'darwin' or platform.machine() != 'arm64' or os.geteuid() == 0:
        raise SafetyError('requires an unprivileged Apple Silicon macOS user')
    if int(platform.mac_ver()[0].split('.')[0]) < 14:
        raise SafetyError('save/restore validation requires macOS 14 or newer')
    helper = HERE / '.build' / 'helmr-vz-probe'
    if helper.is_symlink() or not helper.is_file():
        raise SafetyError('build the helper with dev/macos/build.sh first')
    if not shutil.rmtree.avoids_symlink_attacks:
        raise SafetyError('fd-based cleanup unavailable; refusing preparation')
    host_headroom(ROOT.parent, HOST_DISK_RESERVE + IMAGE_LIMIT)
    result = {'mode': args.mode, 'macos': platform.mac_ver()[0], 'arch': platform.machine(),
              'guest_vcpus': 1, 'guest_memory_mib': 512, 'volume_gib': 2,
              'egress': 'disconnected', 'checkpoint_verified': False, 'worker_verified': False,
              'helper_sha256': hashlib.sha256(helper.read_bytes()).hexdigest()}
    with Admission() as admission:
        command = lambda cmd, **kw: bounded_command(cmd, pass_fds=(admission.fd,), cwd=admission.run, **kw)
        image = Image(admission.run, command)
        image.create_attach()
        for name in ('scratch.raw', 'computer.raw'):
            with os.fdopen(private_file(image.mount / name), 'wb') as f:
                f.truncate(64 * MIB)  # fixed-size guest-visible storage inside bounded volume
        if args.mode == 'smoke':
            copy_pinned(args.kernel, image.mount / 'kernel', args.kernel_sha256)
            copy_pinned(args.initramfs, image.mount / 'initramfs', args.initramfs_sha256)
            result['kernel_sha256'] = args.kernel_sha256
            result['initramfs_sha256'] = args.initramfs_sha256
        else:
            with os.fdopen(private_file(image.mount / 'kernel'), 'wb') as f:
                f.write(b'configuration-validation-only; not bootable\n')
        samples = []
        last = [0.0]
        def monitor():
            now = elapsed_clock()
            if now - last[0] >= 1:
                sample = host_headroom(admission.run)
                if image.path.stat().st_size > IMAGE_LIMIT:
                    raise SafetyError('image allocation exceeded allowance')
                samples.append(sample)
                last[0] = now
        output = command([str(helper), args.mode, str(image.mount), str(admission.fd)], seconds=30, tick=monitor)
        report = json.loads(output)
        if report.get('mode') != args.mode or report.get('clean') is not True:
            raise SafetyError('helper did not prove normal completion')
        result['helper'] = report
        write_record(admission.run / 'helper-result.json', report)
        if args.mode == 'smoke':
            with (image.mount / 'smoke-serial.log').open('rb') as log:
                boot_log = log.read(65536)
            result['guest_init_exec_attempt_observed'] = b'Run /init as init process' in boot_log
            if not result['guest_init_exec_attempt_observed']:
                raise SafetyError('VM API started but guest /init execution attempt was not observed')
        image.detach()
        result['cleanup'] = 'helper exited; owned image detached; private artifacts removed'
        result['host_samples'] = {'count': len(samples),
                                 'min_free_memory_bytes': min(s['free_memory_bytes'] for s in samples),
                                 'min_free_disk_bytes': min(s['free_disk_bytes'] for s in samples)}
        admission.complete()
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=['validate', 'validate-nbd', 'smoke'])
    parser.add_argument('--kernel', type=Path)
    parser.add_argument('--kernel-sha256')
    parser.add_argument('--initramfs', type=Path)
    parser.add_argument('--initramfs-sha256')
    args = parser.parse_args()
    if args.mode == 'smoke' and not all((args.kernel, args.kernel_sha256, args.initramfs, args.initramfs_sha256)):
        parser.error('smoke requires both boot files and their explicit SHA-256 hashes')
    def interrupted(*_):
        raise SafetyError('interrupted; unresolved run remains quarantined')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        print(json.dumps(run_probe(args), indent=2, sort_keys=True))
    except (SafetyError, OSError, ValueError, subprocess.SubprocessError) as error:
        print(f'Not qualified: {error}\nUnresolved runs, if any, remain under {ROOT}. '
              'No automatic stale-run cleanup or retry is permitted.', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
