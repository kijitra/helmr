# macOS host safety implementation

Status: standalone safety probe implemented and exercised on M1 Max; full macOS
Worker support and everyday-host qualification are not established.

## Outcome and boundaries

The acceptance condition is that local Helmr can coexist with everyday work on
an explicitly qualified Mac. Start with a standalone, offline native VM probe;
keep the full Worker unavailable until the lifecycle and isolation gates in
[the runtime design](../design/macos-runtime.md) pass. A successful capability
check, boot, or cleanup is not a successful Helmr deployment or checkpoint test.

Implement admission with one per-user stable lock and a durable unresolved-run
marker. Preserve the marker on ambiguous failure, including controller death,
attachment failure, deadline, and forced child termination. Do not recover from
PID files or signal processes by name. Keep the lock open in the helper too.

The live probe uses one vCPU and 512 MiB guest RAM. These are guest limits, not
a hard bound on Virtualization.framework's host memory or CPU consumption.
Require host headroom, monitor pressure, and stop on exceeded thresholds. Put
all writable VM storage in a private, fixed-capacity disk image. Never attach
raw disks, expose host directories, bridge/NAT networking, alter host firewall,
restart the host, or perform host sleep/update/power-loss fault tests.

Detach only a positively identified owned image, without force, after confirmed
VM stop and helper reap. On uncertainty retain the run and block admission.
Successful detach is additional evidence that the image is no longer in use.

## Implementation and validation sequence

1. Implement the admission, private paths, bounded input/output, deadlines,
   durable quarantine, image ownership, and cleanup rules with unit and
   synthetic-process tests.
2. Compile the signed Go helper using Code-Hex/vz v3.8.0 and validate the full candidate device configuration
   without booting: block disks, vsock control, and a disconnected datagram NIC.
3. Exercise owned image creation/attachment/detachment and controller-failure
   handling before booting. Verify actual volume capacity bounds.
4. Run one bounded boot/stop with explicit checksum-pinned boot files. Record
   source/OS/chip, result, resource samples and cleanup, separately from the
   still-unrun save/restore and full-stack cases.
5. Continue Gate 0 checkpoint work only after the safety prerequisites pass.
   The target M3 and Apple Silicon Mac mini configurations each need actual
   qualification. Minimum RAM and supported macOS versions remain unqualified.

## Repository freshness and review

- Target: `kijitra/helmr`, branch `feat/macos-runtime-design`, initial head
  `5989c40445efb6707fa238754b4de42331acda67`.
- Supporting repos: none. Kijitra company playbooks are local workflow references.
- Refreshed: origin and upstream fetched; fork main remains `2148c441`;
  upstream main is `c0c21910` at this check.
- Skipped: pull/rebase of the active work branch; workspace refresh does not
  apply to this isolated `/private/tmp` checkout. No other project is modified.
- Stale-state risk: inspected upstream delta concerns Console/demo/tests, not
  the new standalone macOS probe. No unrelated upstream changes are merged.
- Delivery: one normal PR; Full Review because of host isolation and ownership.
  Independent plan critique required quarantine, inherited lock, bounded-volume
  storage and proof of detach. These corrections are adopted.
- Cross-model review: unavailable (`claude` and Nix are not installed). Use an
  independent Codex reviewer and an explicit parent security pass; record this
  limitation in the handoff. No linked Linear issue; no Linear mutation.

## Sources

- [Apple configuration save/restore validation](https://developer.apple.com/documentation/virtualization/vzvirtualmachineconfiguration/validatesaverestoresupport())
- [Apple socket-backed network attachment](https://developer.apple.com/documentation/virtualization/vzfilehandlenetworkdeviceattachment)
- [Apple VM save/restore and external disk boundaries](https://developer.apple.com/videos/play/wwdc2023/10007/)

The unpublished Swift helper was replaced by the existing Go binding after the
OSS comparison. The probe uses `Stop()` only on disposable test disks; this is
a hard VM stop, not proof of guest shutdown or flushed writes. Production
checkpoint work still requires the guest freeze and disk-consistency protocol.

## Validation evidence (2026-10-15)

The [machine-readable record](../testing/evidence/macos-m1-safety-probe.json)
includes source-file digests, signed helper digest and boot-input digests. Host:
Apple M1 Max, 64 GiB RAM, macOS 26.6.2, SDK 26.5; Go 1.27.1.

| Criterion | Result |
| --- | --- |
| Synthetic admission, crash, lock inheritance, path, output/deadline and detach checks | 24 Python tests pass |
| Existing OSS helper build, static checks and signature | Go build/vet and strict codesign verification pass |
| Full candidate-device API validation without VM creation | Pass; owned image detached and artifacts removed |
| Disk exhaustion remains inside bounded image | Actual `ENOSPC` after 2,104,492,032 bytes written; image remains 2,147,483,648 bytes |
| Controller dies after attachment | Test controller killed; next admission rejected; exact owned image subsequently reconciled |
| Kernel boot progress | Offline 1-vCPU/512-MiB VM reaches the Linux `/init` execution attempt; three-second smoke completes |
| Normal cleanup | VZ stopped, owned helper exited, exact image detached; no owned image remains and only stable lock file retained |
| RAM/disk coherent save/restore, full Helmr deployment | Not implemented or verified by this probe |
| M3 and targeted Apple Silicon Mac mini | Not run |
| Total host overhead, long-duration responsiveness, thermals and minimum memory | Not established |

The first physical attempts exposed a blank-image CLI option error and a nil
serial-input FD incompatibility in the binding adapter. Both were fixed. The
first smoke reached the `/init` execution attempt, but its early-version-string assertion failed;
use the later `/init` execution-attempt marker now. This kernel marker precedes `kernel_execve()` and does not prove successful userspace initialization. Each failed run remained quarantined
until exact ownership and safe normal detach were established. None was silently
counted as a passing run.

Independent code review found and fixed disk-number reuse during detach,
watchdog installation after native construction, undrained network notifications,
and delayed cleanup-capability checking. No blocking code finding remained for
the standalone probe. Cross-model review remained unavailable; this is not a
production security certification.

The user-level acceptance condition remains **not yet met for full Helmr**.
Next implement and qualify coherent RAM/disk save and restore through the reused
binding and guest-control protocol, followed by full-stack capacity/isolation
checks and each target Mac. Do not label the complete macOS runtime ready from
these probe results.
