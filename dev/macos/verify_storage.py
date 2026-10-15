#!/usr/bin/env python3
"""Opt-in real bounded-volume checks. Creates no VM and never touches an existing run."""
import errno
import fcntl
import json
import os
from pathlib import Path
import selectors
import subprocess
import sys
import time

import probe


def command_for(admission):
    return lambda cmd, **kw: probe.bounded_command(cmd, cwd=admission.run, pass_fds=(admission.fd,), **kw)


def quota():
    probe.host_headroom(probe.ROOT.parent, probe.HOST_DISK_RESERVE + probe.IMAGE_LIMIT)
    with probe.Admission() as owner:
        image = probe.Image(owner.run, command_for(owner))
        image.create_attach()
        filler = image.mount / 'quota-fixture.bin'
        written = 0
        full = False
        deadline = probe.elapsed_clock() + 30
        block = b'x' * probe.MIB
        with os.fdopen(probe.private_file(filler), 'wb', buffering=0) as f:
            try:
                while written <= probe.IMAGE_LIMIT:
                    if probe.elapsed_clock() >= deadline:
                        raise probe.SafetyError('quota fixture deadline')
                    written += f.write(block)
                raise probe.SafetyError('volume did not enforce capacity bound')
            except OSError as error:
                if error.errno != errno.ENOSPC:
                    raise
                full = True
        if not full or image.path.stat().st_size > probe.IMAGE_LIMIT:
            raise probe.SafetyError('volume capacity was not proven')
        allocation = image.path.stat().st_size
        filler.unlink()
        image.detach()
        owner.complete()
    return {'enospc_inside_owned_volume': True, 'bytes_written': written, 'image_bytes': allocation}


def child_attach():
    with probe.Admission() as owner:
        image = probe.Image(owner.run, command_for(owner))
        image.create_attach()
        print(json.dumps({'run': owner.run.name, 'device': image.device}), flush=True)
        sys.stdin.read()  # The parent deliberately kills only this directly owned child.


def controller_death():
    probe.host_headroom(probe.ROOT.parent, probe.HOST_DISK_RESERVE + probe.IMAGE_LIMIT)
    # Admission in the child must be the first state mutation. Existing runs block it.
    child = subprocess.Popen([sys.executable, str(Path(__file__).resolve()), '--child-attach'],
                             stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    selector = selectors.DefaultSelector()
    selector.register(child.stdout, selectors.EVENT_READ)
    try:
        if not selector.select(timeout=60):
            raise probe.SafetyError('attachment barrier not reached')
        line = child.stdout.readline(4097)
        if not line or len(line) > 4096:
            raise probe.SafetyError('attachment barrier failed; preserve any unresolved run')
        attached = json.loads(line)
        child.kill()
        child.wait(timeout=5)
        try:
            with probe.Admission():
                raise AssertionError('controller death permitted new admission')
        except probe.SafetyError as error:
            if 'unresolved previous run' not in str(error):
                raise
        # Only this test's successful attachment is reconciled. No helper/VM was
        # ever constructed on the child path, and the owned child has been reaped.
        fd = probe.private_file(probe.ROOT / 'lock', os.O_RDWR)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            marker = json.loads((probe.ROOT / 'active.json').read_text())
            if marker['run'] != attached['run'] or not marker['run'].startswith('run-') or '/' in marker['run']:
                raise probe.SafetyError('test ownership changed; refusing reconciliation')
            owner = probe.Admission()
            owner.run = probe.ROOT / marker['run']
            owner.fd = fd
            image = probe.Image(owner.run, command_for(owner))
            image.device = attached['device']
            image.detach()  # identity recheck, unique mountpoint, no force
            owner.complete()
        finally:
            os.close(fd)
        return {'controller_killed_after_attachment': True, 'next_admission_blocked': True,
                'owned_attachment_reconciled': True, 'vm_created': False}
    finally:
        selector.close()
        probe.stop_child(child)
        child.stdin.close()
        child.stdout.close()
        child.stderr.close()


if __name__ == '__main__':
    if sys.platform != 'darwin' or os.geteuid() == 0 or not probe.shutil.rmtree.avoids_symlink_attacks:
        raise SystemExit('requires unprivileged macOS and safe fd-based cleanup')
    if sys.argv[1:] == ['--child-attach']:
        child_attach()
    elif not sys.argv[1:]:
        print(json.dumps({'quota': quota(), 'controller_death': controller_death()}, indent=2))
    else:
        raise SystemExit('usage: python3 dev/macos/verify_storage.py')
