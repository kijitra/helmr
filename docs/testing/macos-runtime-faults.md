# macOS runtime fault-test specification

Status: test design, not implemented tests or a passing report.
Scope: the [macOS runtime proposal](../design/macos-runtime.md).

## Preliminary capability observation

On M1 Max/macOS 26.6.2, the following Swift configuration returned success from
both validation calls when compiled and ad-hoc signed with the
`com.apple.security.virtualization` entitlement. Run it outside a restricted
process sandbox; that sandbox reported hardware virtualization unavailable.
It intentionally does not boot `/dev/null`, instantiate a VM, or test disks,
network or save/restore. M01/M02 and all other matrix cases remain not run.

```swift
import Foundation
import Virtualization

let configuration = VZVirtualMachineConfiguration()
configuration.cpuCount = 2
configuration.memorySize = 512 * 1024 * 1024
configuration.platform = VZGenericPlatformConfiguration()
configuration.bootLoader = VZLinuxBootLoader(
    kernelURL: URL(fileURLWithPath: "/dev/null"))
try configuration.validate()
try configuration.validateSaveRestoreSupport()
```

The unsigned probe first reported the missing entitlement, and the signed
probe inside the process sandbox reported unavailable virtualization. Those
errors are probe observations, not proof of unsupported hardware or a passed
fail-closed Worker test. Real qualification must record the full configuration
and run the guest workload oracle below.

## Harness and oracle

Use three layers: deterministic lifecycle/backend contract tests; real local
CP/Dispatcher/PostgreSQL/S3 integration; and signed helper/real arm64 Linux VM
tests on a physical Mac. Linux Firecracker regression runs remain separate.
Neither a fake backend nor a passing macOS CLI build qualifies a VM backend.

Each run gets a private directory, random run ID, isolated credentials/buckets,
bounded RAM/disk, helper identity, ports and a cleanup ledger. Fault injection
uses named barriers in the owned test helper/storage proxy/protocol adapter,
not timing sleeps. Never kill by process name or modify the user's VM/disks.
The controller records one event before and after each barrier and persists
the trace outside the tested process. Fault hooks are local-test-only and
cannot be remotely enabled or supplied through a guest request.

Use a guest fixture without a model API. It increments an in-memory counter,
retains a random RAM-only nonce, a child process identity and an open-file
offset, and fsyncs checksummed sequence records to both writable disks. Record
the expected nonce in the external oracle but never feed it back to the guest.
An init marker distinguishes restore from reboot. A controlled host HTTP server
records side effects by command ID and attempt. This detects duplicate fixture
effects; arbitrary external APIs are not promised exactly-once semantics.

After recovery the oracle compares: CP checkpoint/adoption identity, current
owner/generation/epoch, physical helper/VM/network/disk consumers, disk roots,
RAM/process markers, outstanding reservations and observed external effects.
Local metadata or exit code alone is not sufficient. Workload counters and
forbidden network traffic must stop during the hold; allowed guest-control
traffic is distinguished. At most one owner may execute a given instance.

Set explicit deadlines per operation and a suite watchdog. On timeout, collect
the journal and backend state, exclude the exact owned VM, and report failure.
If exclusion cannot be proved, quarantine the private resources and block the
test Worker; do not report cleanup success or reuse its capacity. After every
case, reconcile resources, retain diagnostic evidence, and verify either proven
cleanup or the documented quarantine state.

## Fault matrix

Every row below is required before the affected capability is called supported.
All rows are currently **not run**. L = deterministic lifecycle test;
I = local service integration; M = physical Mac/real VM; X = Linux regression.

| ID | Layer / injection point | Injection | Expected result / oracle |
| --- | --- | --- | --- |
| M01 | M / configuration | Remove entitlement or use unsupported device set | Explicit preflight failure; no Worker readiness or execution; owned handles closed |
| M02 | M / complete cut | Save, kill helper after durable seal, restore in fresh process | RAM nonce, child, descriptor, counter and disks match; init count stays one; old owner absent |
| M03 | L,M / guest freeze before snapshot | Drop preparation response, cancel and retry same identity | Hold retained; capture blocked until identical successful retry; no member execution |
| M04 | L,I,M / capture | Race checkpoint with guest-control installation/activation | Existing continuation fence wins; no reversible pause across ambiguous activation |
| M05 | L,M / disk capture boundary | Kill helper between disk flush/capture and VM save | Partial cut never selectable; prior adopted cut retained; exact resources reconciled |
| M06 | I,M / snapshot serialization | Exhaust bounded test volume or inject short write | No checkpoint adoption; hold retained or exact abort protocol completed; reservations not prematurely released |
| M07 | I,M / local sealing | Kill before and after artifact fsync and manifest rename | Only fully durable coherent sets publishable; partial files quarantined/reclaimed after authority reconciliation |
| M08 | I / each CAS upload and finalize | Timeout, partial response, hash mismatch, dropped acknowledgment | Immutable digest retry or explicit failure; no partial checkpoint visible; no source deletion |
| M09 | L,I,M / CP adoption accepted, reply lost | Disconnect response then restart Worker | Query/retry exact adoption identity; no competing resume or deletion; CP decision is authoritative |
| M10 | L,I,M / capture abort | Lose abort-grant reply or guest acknowledgment | Guest control may run as permitted; members remain sealed until exact acknowledgment and CompleteAbort |
| M11 | I,M / publication vs terminate | Race cancel/termination with publish/adoption | One terminal/continuation decision; no resumed terminated agent; selected cut retained correctly |
| M12 | L,I,M / restore before activation | Lose fresh grant/activation acknowledgment | VM paused or control-only; workload and egress not enabled; exact retry/fence behavior |
| M13 | I,M / competing restores | Submit two owners and repeat restore request | Exclusive generation wins; at most one VM executes; loser resources reclaimed or quarantined |
| M14 | I,M / lease or epoch loss | Disconnect CP, advance authority generation, reconnect old helper | Old owner cannot continue; no stale network route or disk writer; new owner waits for exclusion |
| M15 | M / helper control EOF | Kill Worker while helper remains alive; restart Worker | Socket EOF is not treated as exclusion; helper identity recovered; no admission until recovery succeeds |
| M16 | L,M / ownership recovery | Stale PID reused; wrong socket peer; forged/partial journal | Never signal unrelated process or trust peer; fail closed and quarantine unresolved ownership |
| M17 | M / artifact restore | Truncate, bit-flip or substitute state, disk or manifest | Hash/config verification rejects before execution; no init/reboot fallback; original checkpoint retained |
| M18 | I,M / compatibility | Wrong backend, arch, CPU/memory/device shape or helper version | Admission rejects with actionable reason; no attempt to reinterpret a Firecracker checkpoint as VZ |
| M19 | I,M / host affinity | Try another Mac/user or mismatching key domain | Scheduler refuses mismatch; forced backend test fails closed; original remains recoverable on compatible host |
| M20 | M / software upgrade | Restore supported prior-format fixture and incompatible OS/runtime fixture | Compatible restore passes markers; incompatible restore blocked, retained and reported; no automatic cold start |
| M21 | M / sleep/wake | Sleep host during run, capture, upload and grant wait | Reconcile authority/time before further work; stale grants not extended by host sleep; no duplicate owner |
| M22 | I,M / control-plane/data services | Restart CP/Dispatcher/DB, drop CAS, delay ClickHouse independently | Explicit not-ready/retry behavior; no fabricated adoption; workload state and bounded buffers accounted |
| M23 | M / every lifecycle phase | Try host loopback, LAN, link-local metadata, DNS/IPv6/alternate routes | Denied destinations unreachable; permitted fixture route works; no bypass while restoring |
| M24 | M / egress transport | Kill policy/secret proxy and replay stale owner binding | Fail closed; no direct NAT fallback; replacement requires current binding; readiness revoked |
| M25 | L,I,M / shared secret mediation and macOS adapter | Wrong origin, shared destination IP, redirect, stale identity, missing secret, forged destination metadata or guest proxy bypass | Reuse shared proxy policy tests; original destination comes from host-owned binding; no credential escapes authorized origin; no bypass; snapshot/log evidence contains no host reusable credential |
| M26 | M / storage attachment | Disconnect/reconnect NBD or force attachment error during writes/capture | Known coherent disk generation or blocked recovery; no acknowledged lost writes, cross-owner reuse or early close |
| M27 | I,M / capacity | Exhaust reservations; concurrently build, run, save and restore | Admission bounded by actual CPU/RAM/disk; include staging/encryption/save amplification; reject before unsafe overcommit |
| M28 | M / local launcher | Occupied port, missing binary, migration failure, Ctrl-C and restart | Actionable startup error; stop only owned services; user Docker/Lima and unrelated processes survive |
| M29 | I / local S3 | Concurrent immutable PUT, range read, interruption, missing object | Store semantics match existing clients; digest integrity and retention hold; distinct CAS/platform namespaces |
| M30 | L,I,X / identity and checkpoint format migration | Old Firecracker rows/objects, mixed versions, unknown backend/affinity, missing/extra/wrong-role objects, wrong media types and oversized opaque VM state | Existing IDs/selectors/formats unchanged; new constraints round-trip; invalid manifests and incompatible consumers reject before admission/publication |
| M31 | L,I,M / architecture | amd64-only image, wrong ELF loader, swapped seed/descriptor | Build/deploy rejects before VM admission; no architecture relabeling or implicit translation |
| M32 | I,M / end to end | Local build/deploy; agent asks; helper gone; answer; resume | Same RAM/process state resumes through product API/Console; init once; all execution services local |
| M33 | L,I,M / retry side effects | Drop command reply after controlled server records effect | Surface ambiguous completion; no blind replay; fixture ID dedup does not imply arbitrary API exactly-once |
| M34 | M / repeatability | 20 capture/restore cycles plus bounded concurrent independent instances | No drift in identity/disk invariants or leak in handles/ports/reservations; latency and memory samples retained |
| M35 | L,I,M / interrupted restore | Kill helper and Worker separately after disk attachment, during VM deserialization, and after restore before control handoff; restart and retry same identity | Original checkpoint retained; no member execution/authorized egress; all partial consumers reconciled; exact exclusion or quarantine before a second owner or capacity reuse |

## Execution order and release decision

Gate 0 starts with M01/M02, M05/M06, M12, M15/M17, M23/M24 and M26.
Include M35 at each restore barrier. The experiment must use the intended device set and enforcement transport,
not a simpler VM configuration that omits the failing device. If native restore
or isolated networking fails, stop integration and revise the backend design.

Contract/schema work then implements L/I cases first. Run all M cases on each
configuration claimed as supported: the M1 reference, an M3-equipped Mac and
the targeted Apple Silicon Mac mini configurations. Record model, chip, RAM
and macOS build per run. Run M27/M32/M34 on the smallest memory configuration
claimed as supported, measuring the complete stack including ClickHouse and
build services; include bounded single-VM operation and rejected over-capacity
requests. Do not infer all Mac mini generations from one tested machine.
Run the backend-independent suite and existing Firecracker tests on a real
x86_64 Linux/KVM host; compilation is not that evidence. Each chip/OS/RAM
combination gets an explicit support-matrix row. Unrun combinations remain
unverified. OS upgrade and another-host cases need their respective real
environments, not only mocked error codes.

A qualification record contains source SHA, signed helper digest, guest/runtime
artifact digests, exact device/network configuration, macOS build, chip family,
opaque host/user domain, service versions and state/manifest digests. For each
ID record injection barrier, seed, timestamps, deadline, before/after authority,
oracle observations, resource accounting, pass/fail/blocked and evidence paths.
Sanitize credentials and user data. Keep synthetic fixtures and failure logs
in private run directories; publish only deliberately sanitized evidence.

Pass requires the defined outcome plus proved exclusion or the prescribed
quarantine; a timeout, skipped assertion or mock success is not a pass.
Quarantine is a passing fail-closed outcome only for a case that explicitly
requires it; it is never proof of resource reclamation. All required cases
must pass for supported environments, and performance results must be reviewed
against agreed targets before full macOS support is announced.
