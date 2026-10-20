# macOS host safety implementation

Status: the repaired Unix NBD/LocalVersion checkpoint experiment passed 20/20
physical cycles on the M1 Max and, on 2026-10-20, 20/20 on one M3 Max configuration
([M3 results](#m3-remote-qualification-2026-10-20)). ARM64 artifact contracts and native disk
preparation and [native runtime registration](macos-runtime-identity.md) are implemented. Full macOS Worker support and everyday-host
qualification are not established.

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

## Full-runtime continuation (2026-10-15)

The operator requested completion beyond the probe. Target/source is the same
fork and branch at `be91efd595504e79a9574e3e3e1740316ced746a`; origin fetched and
worktree clean. No supporting repository changed. Keep the active branch instead
of pulling/rebasing unrelated upstream work. Full Review remains required;
independent architecture and host-safety reviewers are engaged. Cross-model
review remains unavailable. The existing PR remains the single delivery unit.

Implementation order:

1. Validate the **actual** candidate storage attachment for the existing online
   Computer capture contract. Raw file snapshots alone cannot replace
   `LocalVersion.Capture` while execution continues. Test VZ's private Unix NBD
   attachment and save/restore configuration support before porting the Worker.
2. Qualify cross-process RAM/disk restoration with immutable captured disks,
   separate writable restore copies, a strict compatibility manifest, guest
   freeze/continue separation, and an independent nonce/counter/child/open-FD/
   cold-boot oracle. Keep fixed storage and explicit time budgets; failures
   retain quarantine. No cold-boot fallback on restore.
3. Implement the host-initiated guest control transport, tracked disk provider,
   isolated artifact verifier and deny-by-default communication adapter.
4. Integrate the backend into Worker startup, recovery, admission, typed runtime
   identity, checkpoint publication and ARM64 artifact builds; retain Linux
   behavior and existing authority checks.
5. Run full local Agent/ask/resume and fault cases, then hardware-specific and
   long-running coexistence qualification. A first checkpoint pass is not the
   completion criterion.

Critical dependencies identified by source review: native verifier requires
Linux cgroup v2; Computer materialization and startup recovery require Linux
NBD; runtime identities and executable validation admit only x86_64; online
Computer capture must not silently become a VM-wide pause; snapshot envelopes
assume Firecracker state and separate RAM. These need explicit implementations,
not successful returns from currently unsupported stubs.


## Checkpoint and disk integration (2026-10-15)

The helper now boots a checksum-pinned ARM64 userspace fixture, creates a child
process holding a random RAM nonce and an open disk FD, freezes and fsyncs both
guest disks, pauses/saves VZ state, stops, and exits. A separate helper restores
into paused state, verifies the original nonce/PID/start time/FD position while
the workload is still frozen, then explicitly continues it. A strict bounded
manifest binds the exact helper, host boot session, configuration and object
hashes. Restore uses separate writable disks and never falls back to a cold boot.
The supervisor checks both disk journals after each helper exits and revalidates
the immutable cut after continuation. The fixture is not Helmr guestd or an Agent.

The [historical raw-NBD result](../testing/evidence/macos-m1-checkpoint-raw-fixture.json)
records one successful cross-process cycle on the M1 Max. Sequence advanced from
2 to 3, boot count remained 1, nonce/PID/start time stayed identical, and FD offset
advanced from 4224 to 4288. The exact owned image was detached and removed.
This record is bound to its old helper binary digest, not the current source.
The prior uncommitted helper source was not archived as a reproducible commit;
the record expressly preserves that provenance limit. The host boot-session ID
is hashed consistently for publication. This is not a 20-cycle qualification.

Native VZ initially rejected the NBD-backed configuration at Start. Adding the
Apple-required network client entitlement alone did not resolve it. Adding
standard bounded NBD INFO/GO negotiation did; the later raw fixture completed.
The failed attempts remained quarantined until helper exit and exact owned
image identity were established, then were normally detached. The result does
not attribute the original failure to the entitlement alone.

Current implementation reuses encrypted `disk.LocalVersion` through the new
`disk.UnixDevice`. It makes an exclusive durable owner/generation claim before
binding; serves only after one lifecycle consumer is bound; permits at most four
sequential clean connections; and retains evidence after close. Fatal storage
errors survive a concurrent cancellation. Truncated requests and protocol faults
terminate the export instead of opening a new session. Disk Close requires
consumer exclusion and joins NBD operations before releasing LocalVersion.

The current helper acknowledges a non-freezing guest flush, captures a root
while the VM remains running, then verifies that this retained root still
contains sequence 1 and excludes later sequence 2 after the next guest flush.
After native Stop, it joins the server, closes and reopens LocalVersion, then
exports a bounded raw inspection copy for the supervisor's disk journal checks.
Restore reseeds a new LocalVersion from the immutable raw fixture cut. This
bridge tests the actual disk transport, but **does not implement production
checkpoint publication, key custody, or encrypted recovery across helper death**.

| Check | Result |
| --- | --- |
| Python admission/manifest/corruption/copy/journal/deadline tests | 37 passed |
| `go test -race ./internal/disk ./internal/nbd` | Passed on Darwin; subsequent UnixDevice fatal/cancellation regression suite also passed |
| Helper build, `go vet`, ad-hoc signature | Passed |
| Guestd build driver, unsupported arch and hostile environment tests | Passed |
| Linux amd64 disk test binary | Compiled; not executed; no KVM claim |
| Actual x86_64 and aarch64 guestd binaries | Built; ELF64 little-endian machine 62/183 checked; ARM build metadata pins `GOARM64=v8.0` |
| Historical raw-NBD fixture checkpoint | One physical cycle passed; separate helper digest recorded |
| Earlier LocalVersion candidate at e4fdd499 | Trial was blocked by headroom; later trial found and repaired a durability gap (see below) |
| Cleanup after earlier rejected trial | No VM/image created; no active marker; only the stable lock remains |
| Repaired synthetic checkpoint repetition | 20/20 passed on this M1 Max; [evidence](../testing/evidence/macos-localversion-checkpoint.json) |
| Production Agent/ask/resume, Linux KVM regression and full fault qualification | Not run |
| M3, Apple Silicon Mac mini, long-duration responsiveness/thermals | Not run |

Independent design/safety reviews found and fixed payload EOF reconnection,
fatal errors masked by cancellation, capture without a guest flush, and missing
retained-root content checks. Cross-model security review remains unavailable.
No memory protection threshold was lowered to obtain a passing trial. The native physical gate and downstream lifecycle/isolation qualification remain
separate; passing the fixture does not establish full runtime readiness. Remaining implementation includes the Worker
backend/lifecycle barriers, isolated artifact verification, controlled network
and secret mediation, backend-tagged identities/checkpoint publication, complete
ARM64 runtime/seed/compiler pipeline, and a genuine local service composition.

The [earlier candidate record](../testing/evidence/macos-localversion-candidate.json)
binds source-file, helper and input hashes to these checks. It explicitly records
physical and Worker gates as unverified.

## Local-only continuation

The operator explicitly reaffirmed that execution and storage must stay on this
Mac. A remote Linux worker is not an acceptable substitute. Origin was fetched
at `e4fdd49918962cc4f218b71d8abb32af80da362b`; the work branch was clean and matched
origin. No other repository is refreshed or modified. Full Review remains in
force; no merge, deployment, spending or host-settings changes are authorized.

Continue the product integration while physical admission is unavailable. The
native qualification gate controls enabling the backend and claiming readiness;
it does not prohibit implementing and independently testing downstream code.
First extend the producer/validator architecture contract consistently across
Computer definitions, runtime ELF verification and compilation. Preserve existing
x86_64 defaults and serialized identities. ARM64 metadata must never authorize
x86_64 binaries or mismatched seed/image platforms. Continue native backend,
isolated verification and local composition afterward. Completion requires a
real local Agent turn with an artifact and restart/resume evidence on this Mac.

### ARM64 artifact contract implementation

The compiler now takes an explicit canonical Runtime target, keeping its legacy
six-argument invocation on x86_64. Metadata production binds that independently
selected target to every image and seed. OCI boundaries map aarch64 to arm64;
ELF validation requires EM_AARCH64 and the ARM loader throughout the dependency
closure. Existing x86 golden bytes and digests are unchanged. The CLI's pinned
builder image is still x86-only; this change does not silently select an image
based on the Mac CPU or enable an unqualified Worker.

Validation: Go definition/artifact/verifier/bundle/builder/platform suites pass
on Darwin. TypeScript compiler/source suites pass (21 tests), including the
actual generated entry's legacy and explicit target invocations; compiler
TypeScript checking passes. A shared compiler-produced ARM fixture passes Go
parsing, metadata production, bundle parsing and runtime admission. Wrong seed,
Program, Runtime, OCI and ELF architectures are rejected. Linux amd64 and arm64
builder test binaries cross-compile; they have not been executed on Linux.
Independent Codex review found no actionable issue. Cross-model review remains
unavailable. Temporary checksum-verified Bun 1.3.13 and Node 24.21.0 were used;
no system toolchain or shell configuration was changed. Locked package install
used `--ignore-scripts`.

### Native storage preparation

Computer allocation now shares authenticated LocalVersion materialization while
selecting the Linux block attachment or the Darwin arm64 Unix attachment.
Partially initialized attachments remain owned on errors. Duplicate preparation
is rejected; lost-owner recovery recognizes `nbd-owner.json` as well as the
existing config, claim and socket markers. All markers, including dangling
symlinks, prevent deletion and capacity release. The allocation owner's operation
mutex remains responsible for serializing the complete prepare/release lifecycle;
the in-flight map is an additional guard, not a replacement.

Darwin `computerhost` tests pass (26.586s); the initial sandbox run failed solely
on Unix socket bind permission and was rerun with that capability. Tests use real
encrypted LocalVersion storage, exercise attachment failure/duplicate/stale
ownership, and verify retained marker/capacity behavior. The Linux amd64 suite
cross-compiles but was not executed. Independent security review found no
blocking issue. Worker admission, native recovery, verifier and preparation-stage
qualification remain fail-closed. No VM was launched by these unit tests.

### Native write durability repair

The LocalVersion physical trial ran after host headroom recovered and caught a
real failure before restore: both guest disks reported sequence 2, but reopening
the Computer disk exposed only sequence 1. A bounded diagnostic replay observed
sequence 2 in LocalVersion memory before Stop, three NBD WRITEs and zero NBD
FLUSH requests, despite the VZ attachment selecting full synchronization. This
identifies the failed boundary in this fixture; it does not establish why VZ or
the raw guest block path omitted FLUSH. Both failed runs confirmed native Stop,
reaped the helper and normally detached only the exact owned image after
preserving evidence. No force-detach or unrelated VM operation was used.

UnixDevice now durably commits each complete WRITE/TRIM before a successful
reply, holding the request mutex through persistence. Linux block attachments
keep buffered writes and explicit FLUSH. The fixture still closes and reopens
LocalVersion without a teardown flush. A subsequent physical cycle passed in
7.240 seconds, preserving RAM nonce, PID, start time, open FD and both disk
journals across separate helper processes; source sequence 2 continued to 3.
Both helpers exited and the exact image was detached and removed.

The full Darwin disk/NBD race suite passes. Protocol tests require complete
EIO/ENOSPC replies on injected persistence failure, including failures at staged
objects, synced root, renamed root and committed directory boundaries. No-client-
FLUSH WRITE/TRIM survives reopen; online capture cannot split a logical request;
Linux buffered behavior is covered unchanged. Independent review found one test
that incorrectly allowed a missing error response; it was tightened and passed.

Limitation: durable Unix mutations wait behind an in-progress capture commit,
including its encoding. The VM remains runnable and reads can proceed, but the
buffered backend's write progress during encoding is not preserved. Write
amplification, latency under realistic workloads, ext4 syncfs, full Worker
lifecycle, M3/Mac mini and long-duration host responsiveness remain unqualified.


The reviewed repair then passed **20/20 consecutive physical cycles** with the
same helper, kernel and initramfs. Each cycle took 5.620–7.759 seconds and used
1 vCPU, 512 MiB RAM and a bounded 2 GiB private image. Every cycle verified both
disk journals after LocalVersion reopen and removed only its own image after
normal detach. No unresolved run marker remains. The [machine-readable evidence](../testing/evidence/macos-localversion-checkpoint.json)
records source hashes, binary identity, embedded build provenance, all cycles and
the pre-repair diagnostic failure. This is repetition of the synthetic fixture;
it does not qualify the production Agent or the complete fault matrix.

The native macOS CLI was also built and its `--help` executed locally. It still
needs a local Control Plane and functioning Worker; a CLI binary is not a running
Helmr installation. No remote service login or external model call was made.


## Native runtime registration (2026-10-15)

Backend-tagged Apple VZ identity, CPU evidence matching, a new database migration
and immutable pool matching are implemented. Existing Firecracker identity bytes
remain unchanged. PostgreSQL 18.6 migration, conflict, NULL-edge, downgrade and
reactivation tests pass with existing lifecycle/admin API regression checks:
42 top-level tests, 260 test records, none skipped, race detector enabled.
Both independent reviewers report no remaining actionable finding for this
slice. See [scope, compatibility and the same-boot limitation](macos-runtime-identity.md).
This does not enable the native Worker or publish native checkpoints.

## M3 remote qualification (2026-10-20)

The existing experiment was built and operated over SSH on an Apple M3 Max,
Mac15,9, 128 GiB RAM, macOS 27.0.1 / build 26A434. A separate GPU training workload
remained running. This is one observed coexistence case, not a responsiveness,
thermal or minimum-resource qualification.

The first configuration check failed before helper launch: `bounded_command`
combined stderr with the plist emitted on stdout by `hdiutil attach`. The image
and unresolved marker remained in place. After confirming controller exit and
the exact image, device, mountpoint and owner under the stable lock, the known
image was normally detached, absence was rechecked, and only that run's private
artifacts were removed. No forced detach, generic recovery, other VM stop or
host setting change was used.

The repair in `cbeb139750485bf1105c23abd67e12d01793d986` drains stdout and stderr
independently and returns stdout alone. Both channels share the original output
limit. Deadlines and pressure checks now remain active if the child closes its
output pipes before exiting; only the owned child can be terminated. Malformed
stdout and failed commands still retain quarantine. Regression tests first
failed against the old implementation, then passed on the local and remote Macs.

| Check | Result |
| --- | --- |
| Python admission, command and checkpoint tests | 47 passed on each Mac |
| Configuration and NBD save/restore API validation | Passed on M3; no VM started for these checks |
| ARM64 boot smoke | Passed; `/init` execution attempt observed, not full guest readiness |
| Fresh-process LocalVersion checkpoint cycles | 20/20 passed, each 5.327–7.002 seconds |
| RAM/process/FD continuity and coherent disk journals | Verified in each cycle; retained online cut unchanged after later writes |
| VM/process exclusion and owned image cleanup | Verified for every cycle; no unresolved marker remained |
| Owned-volume exhaustion | `ENOSPC` after 2,104,492,032 bytes; image stayed at 2,147,483,648 bytes |
| Test controller death after attachment | Next admission refused; exact known attachment reconciled; no VM created |
| Darwin disk/NBD/computerhost/identity/API race suites | Five packages passed; 225 top-level tests passed, one Linux-only test skipped; 564 passing records including subcases |
| Remote source provenance | All 2,658 tracked source files matched the repair commit; helper SHA and fixed Go/kernel/input hashes recorded |

The skipped `TestPreparedProgramMemoHitPreservesSnapshotAndTargetAuthority`
requires Linux artifact snapshots. It is not a macOS verifier qualification.
Linux execution/KVM tests and the full repository suite were not run. An
independent Codex plan, code and explicit security-boundary review found no
actionable issue in this repair; the Nix/Claude review wrappers were unavailable.

[Sanitized M3 evidence](../testing/evidence/macos-m3-localversion-checkpoint.json)
contains exact source and binary hashes, per-cycle results, fault checks and
retained raw-artifact hashes. Raw boot-session identifiers, private host paths
and workload data are omitted.

The production Worker is still unavailable on macOS. Its startup remains tied
to Firecracker, Linux artifact verification and Linux network cleanup. The next
integration must preserve guest continuation authority, isolated verification,
network/secret enforcement and checkpoint publication; it must not bypass those
checks to obtain a successful startup. Actual Agent turns, ask/resume, production
ext4 workloads, long-duration host behavior, other M3 configurations and Mac mini
remain unverified.
