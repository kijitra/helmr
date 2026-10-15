#!/usr/bin/env python3
"""Build a source-owned ARM64 checkpoint oracle initramfs from a pinned Alpine APK.
No APK installation, package scripts, or archive paths are executed/extracted.
"""
import argparse
import gzip
import hashlib
import io
import json
import os
import stat
from pathlib import Path
import struct
import tarfile
import zlib

APK_SHA256 = '1cb07a2d56c685194cadde1c2f436a71eb4fd1aeab9458c4bf249e2b467f5bdb'
PREFIX = 'lib/modules/6.18.56-0-virt/kernel/'
MODULES = {
    'virtio_blk': PREFIX + 'drivers/block/virtio_blk.ko.gz',
    **{n: PREFIX + 'net/vmw_vsock/' + n + '.ko.gz' for n in (
        'vsock', 'vmw_vsock_virtio_transport_common', 'vmw_vsock_virtio_transport')},
}
LIMIT = 128 * 1024 * 1024


def read_regular(path,limit):
    fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
    with os.fdopen(fd,'rb') as f:
        info=os.fstat(f.fileno())
        if not stat.S_ISREG(info.st_mode) or not 0<info.st_size<=limit:
            raise ValueError('input must be a bounded regular file')
        raw=f.read(limit+1)
        if len(raw)>limit:raise ValueError('input grew beyond bound')
        return raw


def expand(raw):
    d = zlib.decompressobj(31)
    result = d.decompress(raw, LIMIT + 1)
    if len(result) > LIMIT or not d.eof:
        raise ValueError('gzip member is truncated or exceeds bound')
    return result


def cpio(entries):
    result = bytearray()
    for inode, (name, mode, data) in enumerate([*entries, ('TRAILER!!!', 0, b'')], 1):
        encoded = name.encode() + b'\0'
        fields = [inode, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(encoded), 0]
        result += b'070701' + ''.join(f'{n:08x}' for n in fields).encode() + encoded
        result += b'\0' * (-len(result) % 4)
        result += data
        result += b'\0' * (-len(result) % 4)
    return gzip.compress(bytes(result), mtime=0)


def build(apk, fixture, output):
    raw = read_regular(apk,60_000_000)
    if len(raw) > 60_000_000 or hashlib.sha256(raw).hexdigest() != APK_SHA256:
        raise ValueError('APK does not match pinned Alpine linux-virt 6.18.56-r0')
    needed = {'boot/vmlinuz-virt', *MODULES.values()}
    data = {}
    with tarfile.open(fileobj=io.BytesIO(raw), mode='r:gz', ignore_zeros=True) as archive:
        for entry in archive:
            if entry.name in needed:
                if entry.name in data or not entry.isfile() or entry.size > LIMIT:
                    raise ValueError('invalid or duplicate archive member')
                data[entry.name] = archive.extractfile(entry).read(LIMIT + 1)
    if set(data) != needed:
        raise ValueError('missing kernel or modules')
    compressed_kernel = data['boot/vmlinuz-virt']
    offset = compressed_kernel.find(b'\x1f\x8b\x08')
    if offset < 0:
        raise ValueError('expected gzip Linux EFI zboot image')
    kernel = expand(compressed_kernel[offset:])
    if kernel[56:60] != b'ARM\x64':
        raise ValueError('not an ARM64 Linux Image')
    binary = read_regular(fixture,LIMIT)
    if len(binary) > LIMIT or binary[:4] != b'\x7fELF' or struct.unpack_from('<H', binary, 18)[0] != 183:
        raise ValueError('fixture must be an ARM64 ELF')
    entries = [(n, 0o040755, b'') for n in ('dev', 'proc', 'sys', 'modules')]
    entries.append(('init', 0o100755, binary))
    for name, path in MODULES.items():
        entries.append(('modules/' + name + '.ko', 0o100644, expand(data[path])))
    ramdisk = cpio(entries)
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    (output / 'Image').write_bytes(kernel)
    (output / 'initramfs').write_bytes(ramdisk)
    record = {'apk_sha256': APK_SHA256, 'fixture_sha256': hashlib.sha256(binary).hexdigest(),
              'kernel_sha256': hashlib.sha256(kernel).hexdigest(),
              'initramfs_sha256': hashlib.sha256(ramdisk).hexdigest(),
              'kernel_release': '6.18.56-0-virt',
              'source': 'https://dl-cdn.alpinelinux.org/alpine/v3.23/main/aarch64/linux-virt-6.18.56-r0.apk'}
    (output / 'inputs.json').write_text(json.dumps(record, indent=2) + '\n')
    print(json.dumps(record, indent=2))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apk', type=Path, required=True)
    parser.add_argument('--fixture', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    build(args.apk, args.fixture, args.output)
