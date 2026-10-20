# Bounded macOS VM experiment

This is an **experimental safety probe**, not a macOS Helmr Worker. It uses
[Code-Hex/vz v3.8.0](https://github.com/Code-Hex/vz/tree/v3.8.0), the Go binding
used by Lima and vfkit. No Swift or custom VMM implementation is needed.

Build on an Apple Silicon Mac with Go 1.27.1 and Apple's Command Line Tools (or
Xcode). This repository pins Go through the module; no system installation or
service is performed by these scripts.

```sh
./dev/macos/build.sh
python3 -m unittest discover -s dev/macos -v
python3 dev/macos/probe.py validate
```

`validate` creates a private bounded disk image, checks the full candidate device
configuration and save/restore API support, then detaches and removes its own
artifacts. It **does not start a VM**. A dummy kernel is used only for configuration
validation, never as boot evidence. It requires Python 3.10+ with fd-based
`shutil.rmtree` support. Run as a regular user, never through `sudo`.

The optional `smoke` mode accepts explicit real ARM64 Linux boot inputs:

```sh
python3 dev/macos/probe.py smoke \
  --kernel /absolute/canonical/path/to/uncompressed-Image \
  --kernel-sha256 LOWERCASE_SHA256 \
  --initramfs /absolute/canonical/path/to/initramfs \
  --initramfs-sha256 LOWERCASE_SHA256
```

Input paths must have no symlink components (use `/private/tmp`, not `/tmp`).
Each file is limited to 128 MiB; the copied bytes are hashed before launch.
There are no automatic downloads, environment credentials or model API calls.
The signed helper runs for three seconds, then uses VZ's hard stop on disposable
disks. The runner requires the kernel's `/init` execution-attempt marker. Linux emits it before `kernel_execve()`, so it proves neither successful userspace initialization, guest clean shutdown, nor application readiness. It
performs no checkpoint, external-network test or full-stack startup.

## Implemented safeguards

- One VM per user through a stable inherited `flock`, plus a durable unresolved-run
  marker written before image creation/attachment. No PID-file takeover.
- One vCPU, 512 MiB guest RAM, two fixed 64 MiB guest disks. These constrain the
  guest, **not total host CPU/RAM**. macOS and VZ have additional overhead.
- A dedicated 2 GiB APFS disk image bounds guest disks, boot input copies and logs.
  Reserve 20 GiB of host free space plus the image allowance before preparation.
  The image is not a host raw disk and no host directory is shared with the VM.
- Require normal memory pressure and at least 2 GiB of free/speculative memory;
  sample during helper execution. These are conservative admission/tripwire
  values for the experiment, not a measured product minimum RAM requirement.
- Disconnected AF_UNIX datagram network attachment. No NAT, bridge, external
  route, exposed port, host firewall change, or guest-triggered vsock listener.
- Bounded subprocess output and command deadlines. Helper lifetime has an
  independent 15-second bound for smoke (30 seconds for checkpoint), using a clock that includes host sleep.
- Stop only direct owned subprocesses. No `killall`, process-name search,
  host reboot, forced image detach, or changes to existing Docker/Lima VMs.
- On success, confirm VM stopped (when booted), reap the helper, verify the image
  path/device/mount identity, detach normally, recheck attachment absence, and
  remove only the owned private directory. Keep the stable lock file.
- On any failure/ambiguity, preserve `/private/tmp/helmr-macos-UID/active.json`
  and the run directory; **block the next invocation**. No automatic retry,
  forced detach, recursive cleanup of guessed paths, or stale PID signaling.

The runner trusts its own checkout and local user; it is not a sandbox against
malicious host code running as that same user. Build caches and toolchain downloads
are separate development artifacts, not covered by the run-volume quota.

The opt-in physical storage test (no VM) is:

```sh
python3 dev/macos/verify_storage.py
```

It fills only the dedicated volume until `ENOSPC`, then separately kills its
own test controller after successful attachment. It verifies blocked admission
and reconciles only that test's known attachment; it is not a generic recovery
command and refuses pre-existing unresolved runs.

## Unresolved runs

An unresolved marker is deliberate. Do not remove it merely because a PID is
absent. Inspect the private `attachment.json` and `hdiutil info -plist`, establish
that any attachment belongs to this exact image, and establish that no VM or
other consumer remains. Recovery is currently manual and must not force detach.
If ownership cannot be established, retain the evidence and do not start another
probe. No automatic recovery command is provided yet.

## Qualification

Synthetic tests, configuration checks, VM boot, coherent save/restore and product
acceptance are distinct. See [implementation evidence](../../docs/implementation/macos-host-safety.md)
and [the fault matrix](../../docs/testing/macos-runtime-faults.md). A passing
probe must not be presented as "safe on all Macs" or as complete Helmr support.
M3 and each claimed Apple Silicon Mac mini configuration require real evidence.

## Cross-process checkpoint and disk transport experiment

The checkpoint helper exercises Helmr's encrypted `LocalVersion` using a private
Unix NBD export. The NBD server supports bounded INFO/GO as well as the existing
EXPORT_NAME negotiation. Its consumer claim is exclusive and never recovered by
unlinking an old socket. A fatal storage fault or truncated request ends the
export; normal VM stop joins the server before releasing disk ownership.

Build the synthetic Linux ARM64 fixture separately from production guestd:

```sh
(cd dev/macos/fixture && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOARM64=v8.0 \
  go build -trimpath -o ../.build/checkpoint-fixture .)
python3 dev/macos/build_fixture.py \
  --apk /absolute/path/to/linux-virt-6.18.56-r0.apk \
  --fixture dev/macos/.build/checkpoint-fixture \
  --output dev/macos/.build/checkpoint-inputs
```

Use the official Alpine v3.23 aarch64 package at
`https://dl-cdn.alpinelinux.org/alpine/v3.23/main/aarch64/linux-virt-6.18.56-r0.apk`,
SHA-256 `1cb07a2d56c685194cadde1c2f436a71eb4fd1aeab9458c4bf249e2b467f5bdb`.
The builder verifies this exact package, copies only the kernel and four named
modules, and constructs an initramfs without extracting archive paths or running
package scripts. It prints input hashes and refuses an existing output directory.

After building the signed helper with `build.sh`, pass the printed hashes:

```sh
python3 dev/macos/checkpoint.py \
  --kernel /absolute/canonical/path/to/checkpoint-inputs/Image \
  --kernel-sha256 PRINTED_KERNEL_SHA256 \
  --initramfs /absolute/canonical/path/to/checkpoint-inputs/initramfs \
  --initramfs-sha256 PRINTED_INITRAMFS_SHA256
```

This uses the same one-VM admission, 512 MiB RAM and 2 GiB private image. Each
checkpoint helper has a 30-second independent watchdog. The supervisor checks a
180-second overall deadline between phases and I/O chunks; this does not promise
preemption of a blocked filesystem syscall. All host headroom checks remain in
force. An admission failure before resource creation launches no VM.

The fixture acknowledges a non-freezing flush, captures a disk root while the VM
runs, then checks that later writes did not modify the retained cut. It freezes
both disks before RAM save, excludes the original helper, and restores in a new
process. It compares a RAM-only nonce, child PID/start time, open-FD offset,
checksummed journals and cold-boot count before permitting continuation. The
supervisor checks disk journals after each helper exits and checks that the
sealed cut was not modified. Restore errors never trigger a cold boot.

Raw disk export/reseed remains an explicitly bounded **fixture bridge**. It does
not implement production encrypted checkpoint publication or key recovery. The
experiment does not run the actual guestd or an Agent. The production guestd
build driver separately accepts `ARCH=aarch64` with baseline `GOARM64=v8.0`;
complete ARM64 runtime/seed/compiler artifacts are still required for execution.

The repaired LocalVersion helper completed 20/20 physical checkpoint cycles on
the M1 Max. A prior trial caught a missing durability boundary and was corrected
before repetition. See [the recorded evidence](../../docs/testing/evidence/macos-localversion-checkpoint.json).
On 2026-10-20 the same bounded experiment also passed 20/20 cycles on an M3 Max,
Mac15,9 with 128 GiB RAM and macOS 27.0.1. The [M3 evidence](../../docs/testing/evidence/macos-m3-localversion-checkpoint.json)
also records the command-stream parsing repair, storage faults and one Linux-only
test skip. Full Worker execution, production ext4 workloads, other M3
configurations and Mac mini remain unverified.


The later LocalVersion trial found and repaired a durability gap: the raw guest
block fixture delivered WRITEs but no NBD FLUSH. Unix exports now persist each
mutation before successful reply. The passing cycles
still do not qualify the full Worker or ext4 workload. Writes may wait
behind capture encoding. See the latest implementation evidence for results.
