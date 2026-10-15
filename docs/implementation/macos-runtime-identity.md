# Native runtime identity integration

This change adds the contract needed to register a native Mac runtime. It does
not enable a native Worker, publish a VZ checkpoint, or establish that Helmr can
run a real job. The current VM qualification evidence remains the bounded
20-cycle fixture described in `macos-host-safety.md`.

## Scope and review

One existing branch/PR, based on `d73b814c`, with no supporting repository changes.
The branch was clean and matched origin before this slice. Do not rebase unrelated
upstream changes into the implementation. Full Review applies to compatibility,
identity and persistence. Independent plan review accepted the tagged selector
with conservative same-boot affinity. Cross-model review remains unavailable.

## API and schema inventory

| Surface | Behavior in this slice |
| --- | --- |
| `vmplatform.Profile` | Keep Firecracker v0 hash bytes/IDs and omitted backend wire fields; add explicitly tagged `apple-vz` v1 and typed native payload |
| Worker activation JSON | Existing strict decoder rejects unknown fields; runtime and CPU evidence must agree on native helper, OS build, model and kernel |
| CPU environment digest | Preserve Firecracker v0 digest; native v1 uses its own domain and fields rather than empty legacy values |
| Sealed Worker pool | Exact runtime ID still required; affinity changes cannot rewrite the pool |
| Database `vm_platforms` | Migration 2 backfills Firecracker/x86/v0 tags without changing IDs; legacy-only fields become nullable only for native rows |
| Generated queries | sqlc 1.31.1 regenerates nullable columns and native fields; upsert conflicts compare every identity field and only refresh time on equality |
| Downgrade | Refuse while any native identity exists; never delete or coerce native rows |
| Console | No runtime-profile TypeScript declaration or direct renderer found; no Console change needed in this slice |
| Checkpoint manifests, scheduling and publication | Existing Firecracker layout still applies. Native layout and explicit checkpoint affinity admission remain separate unfinished work |
| Worker startup / readiness | Existing Linux backend and recovery guards remain; this change does not enable native activation in the executable |

The new payload binds the signed helper digest, OS build, model, opaque local
installation digest, key-domain digest, boot-session digest and explicit state
format. Shared kernel/initramfs/rootfs/descriptor digests are still mandatory.
No hardware serial or raw boot-session identifier is put into the profile.
These fields constrain compatibility; they are not remote attestation.

The native profile requires `aarch64`, `helmr.vm-runtime.v1` and
`helmr.apple-vz-state.v1`. Unknown versions, mixed backend fields and incomplete
native payloads are rejected. A pre-change Control Plane also rejects native
activation through its unknown-field and legacy-contract checks. Deployment of
compatible CP/Worker code is still required before enabling a native pool.

## Conservative affinity and lifecycle limitation

Until across-reboot restoration is qualified, a boot-session digest is part of
the native runtime ID. A Worker process restart during the same boot may
reactivate its sealed pool. A host reboot, different installation or key-domain
rotation produces a different runtime ID and cannot reactivate that pool. A new
pool must be explicitly enrolled; old checkpoints remain unavailable and are
retained. There is no silent pool rewrite or cold-start fallback. This is broader
than checkpoint-only affinity and does not claim reboot recovery.

## Validation

- Existing Firecracker runtime-ID golden and CPU-environment golden unchanged.
- Native JSON fixture round trip, binding of every compatibility field, invalid
  and mixed selectors, mismatched host evidence and unknown JSON fields tested.
- PostgreSQL 14.15 on this Mac cannot parse the existing migration 1. The first
  integration run failed before the new migration; it is not counted as passing.
- PostgreSQL 18 is required by the repository toolset. Tests use a dedicated
  temporary cluster, with no existing databases or host installation changes.
- Official PostgreSQL 18.6 source archive SHA-256 was verified before building
  into `/private/tmp/helmr-postgresql-18.6`; no system installation was replaced.
- Real PostgreSQL 18.6 migration, backend conflict, NULL-edge rejection,
  downgrade preservation/refusal and same-boot reactivation tests pass.
- Catalog regenerated and its 8 additions / 3 removals reviewed, then normal
  catalog round-trip and all generated-query preparation checks pass without
  update mode. Selected existing pool/lifecycle/activation/admin API regressions
  also pass under the race detector (schema 5.261 s, workergroup 8.497 s,
  controlplane 2.908 s). No selected test was skipped. Temporary cluster stopped.
- Complete `vmplatform`, `workerapi` and Darwin-available `firecracker` race
  suites pass. Linux/KVM runtime tests remain unexecuted.
- Independent reviews found the stale catalog and a newly aliased native payload
  in the otherwise detached Worker template. Both are fixed; the template now
  copies the payload and a bidirectional mutation regression passes.
- Native runtime registration and schema compatibility are verified separately
  from native Worker operation, which remains incomplete.
