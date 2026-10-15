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
  independent 15-second bound using a clock that includes host sleep.
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
