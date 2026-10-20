# Reuse of existing macOS VM implementations

Inspected 2026-10-15. These are implementation references and candidate
components, not evidence that Helmr runs on the target Macs.

## Choice for the first experiment

Use `github.com/Code-Hex/vz/v3` **v3.8.0**, tag commit
`c03d09ab2449b492aca582d4c1d8805270f36d87`, in an isolated Go helper. Both
Lima and vfkit use this binding. It already implements configuration validation,
Linux boot, disks, sockets, pause, RAM-state save/restore and stop. Avoid
maintaining a separate Swift bridge or building a new VMM.

Keep the Helmr-specific lifecycle, authority grants, encrypted publication,
secret proxy, capacity admission and recovery contracts. The binding supplies
Apple API access; it does not supply these application guarantees.

## Compared implementations

| Component | Useful existing implementation | Boundary for this project |
| --- | --- | --- |
| [Code-Hex/vz v3.8.0](https://github.com/Code-Hex/vz/tree/v3.8.0) | Go bindings including `SaveMachineStateToPath`, `RestoreMachineStateFromURL`, block devices, vsock and socket-backed networking | Selected for the experiment. Still requires complete-device and physical restore tests. No deterministic public VM `Close`; one helper per VM. |
| [Lima VZ driver](https://github.com/lima-vm/lima/blob/master/pkg/driver/vz/vz_driver_darwin.go) | Established VM configuration, guest communication and lifecycle patterns | Inspected VZ `CreateSnapshot`/`ApplySnapshot` return `errUnimplemented`. Its CLI snapshot command is not evidence of native VZ RAM restore. Useful for backing services; do not inherit host-sharing/network defaults for workloads. |
| [vfkit](https://github.com/crc-org/vfkit/tree/22b7610767167e5060d1c90a477f106ef6234a20) | Go VM runner used by Podman/CRC; device and lifecycle implementation | Current REST API exposes pause/resume/stop but no save/restore endpoint; [issue 449](https://github.com/crc-org/vfkit/issues/449) remains open at inspection. Reuse the underlying binding directly rather than add an API server solely for the probe. |
| [libkrun](https://github.com/libkrun/libkrun) | Small VM/process isolation library with macOS ARM64 HVF support | Candidate for lightweight execution. RAM-preserving lifecycle equivalence was not established by this inspection; do not substitute a disk-only snapshot. Its main branch is evolving toward a new major API. |
| [firecracker-macos](https://github.com/AitorConS/firecracker-macos/tree/1e704adb9b51cf6bf1d29bb8e3c793ef3054f0fb) | Independent experimental HVF port; local snapshots, default-deny egress, resource limits, lifecycle/security tests | Valuable reference and alternative. README qualifies only M5/macOS 26+; its API/security/snapshot contracts differ from upstream Linux Firecracker. No M1/M3/Mac mini support or Helmr drop-in compatibility is inferred. |

Upstream [Firecracker requires Linux KVM](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md).
A similarly named macOS fork does not inherit upstream security qualification.
No reference project's install, VM, or fault-test script was executed.

## Binding limitations handled by the helper boundary

The v3.8.0 lifecycle/save/restore methods wait on native callbacks without a
context cancellation parameter. A supervisor deadline therefore quarantines
and terminates the helper; it must not launch a second, conflicting operation
because one Go goroutine timed out. Explicitly check both the boolean and error
from `Validate()` and `ValidateSaveRestoreSupport()`.

Drain state and network notifications; the binding uses unbounded notification
channels. The first control transport should be one host-initiated connection
to a guest listener with bounded retries, avoiding the guest-initiated listener's
unbounded acceptance path. The disconnected smoke probe creates no listener.

Construct the AF_UNIX datagram pair internally. Do not accept arbitrary network
FDs, NAT/bridge options, host block devices or shared directories. Closing the
original socket is not a revocation guarantee because the bridge duplicates it.

`RequestStop()` is only a request. `Stop()` forcibly stops a VM and is not proof
of guest fsync. The disposable smoke probe can use `Stop()`; persistent workload
capture must still freeze the guest, capture coherent disks and confirm physical
exclusion. Process exit and successful verified image detach are separate
cleanup evidence.

The binding API's existence and a successful build do not satisfy Gate 0. Keep
M1/M3/Mac mini physical validation, checkpoints, performance and full-stack
qualification explicitly unrun until evidence exists.
