# Crossplane v1 → v2 Live Migration: Decision Axes

A vendor-neutral breakdown of the independent decisions any live-migration design has to make, drawn
from the approaches people have already built.

Each axis below states the question, the options that exist, and what follows from each, laying out
the choices for a design effort to weigh without recommending among them.

---

## Scope

A full v1 → v2 migration is two separable jobs. This document covers only the second.

**Authoring (out of scope).** Rewriting the definitions themselves into v2 shape. XRDs move to
`apiextensions.crossplane.io/v2` with `scope`, lose the X-prefix, and drop `claimNames`. Compositions
and functions move to the namespaced provider API groups (`.aws.upbound.io` → `.aws.m.upbound.io`),
swap `deletionPolicy` for `managementPolicies`, add `providerConfigRef.kind`, and compose connection
secrets.
This is a source transformation that changes files in git and produces no change to any running
resource. It is mechanical and largely deterministic, and official docs and tooling already cover it.
The one part that cannot be automated (rewriting composition-function business logic) is manual
regardless of migration design. This document treats it as a prerequisite and does not enumerate decisions for it.

**Adoption (in scope).** Moving a running control plane's existing XRs, managed resources, and the
cloud resources they manage onto the v2 definitions without recreating anything in the cloud. This has
no official tooling and is where the design decisions below live.

---

## Constraints every design must respect

These rules follow from how Crossplane and cloud providers behave. They hold regardless of which options a
design selects below, and they are the criteria any adoption design is measured against.

- **External-name identity.** A managed resource is bound to its cloud resource by the
  `crossplane.io/external-name` annotation. A v2 MR must carry the correct external-name before the
  provider reconciles it with write authority. Otherwise the provider creates a new cloud resource or
  fails to adopt the existing one.
- **Orphan before delete.** Deleting a v1 MR that still holds delete authority deletes its cloud
  resource. The cloud resource survives v1 teardown only if the MR's policy is first changed to orphan
  or observe (removing Delete).
- **Single write authority.** If two controllers both hold write authority over the same external
  resource, they can conflict and thrash. A non-disruptive migration must ensure at most one
  write-active manager per cloud resource at any instant.
- **Composition becomes authoritative.** Once the v2 composition manages an MR, it renders
  `spec.forProvider` on every reconcile and overwrites values that do not match its output. Whatever
  authority transfer looks like, the v2 composition's rendered output becomes the resource's desired
  state at that moment.
- **Connection-secret continuity.** v2 removes built-in XR connection-secret support. Secrets are
  produced explicitly. Downstream consumers depend on the connection secret existing and being
  populated across the handoff.

---

## Decision axes

The axes are ordered to follow the migration flow: the up-front choices, validating the v2
configuration before any live change, then bringing the resources up safely, matching them to their
v1 counterparts, and adopting.

### Target scope and MR object strategy

**Decision.** Does the migration move managed resources to their namespaced (`.m.`) kinds, or leave
them cluster-scoped and move only the XR? This choice drives how the v2 MR object comes to
exist.

**Options.**
- **Namespaced MRs.** The v2 MR is a different kind and CRD than the v1 MR. The existing object cannot
  be relabeled into it, so a new v2 MR object is created and the external-name is transferred onto it
  (the external-name placement axis), then the v1 object is orphaned and removed. Reaches the fully namespaced v2 model.
- **Cluster-scoped MRs.** The XR moves to v2 while the MRs keep their v1 cluster-scoped kinds. The
  existing MR objects are reparented (owner reference and composite label repointed to the v2 XR).
  The external-name never moves because the object is preserved. Does not reach the namespaced model
  for MRs.

**Consequences.** Reparenting is only available when the MR kind is unchanged, so it is tied to the
cluster-scoped option. The namespaced option is the only one that reaches the v2-native end state, and
it is the only one that introduces the external-name transfer problem (the external-name placement axis). Cluster-scoped MRs
remain supported under v2 backward compatibility.

### Logic location and trigger

**Decision (a): where the migration logic lives.**
- **In-band.** The migration is expressed as composition or function logic (a toggle, a captured data
  structure, a function that stamps external-name). Because it is part of the desired state, it is
  reasserted by reconciliation rather than fought by it.
- **Out-of-band.** A CLI, controller, script, or manual `kubectl` performs the steps against the
  cluster from outside the composition.

**Decision (b): how the migration is triggered.**
- **Imperative CLI** run by an operator.
- **Manual runbook** of hand-run `kubectl` commands.
- **GitOps PR**, where the migration is a commit and a reconciler applies it.
- **Declarative controller or CRD**, where intent is declared and a controller executes asynchronously.

**Consequences.** Out-of-band mutations applied to a GitOps-managed cluster diverge from the source of
truth and can be reverted by the reconciler unless sequenced against it. In-band logic and GitOps-PR
triggers do not create that divergence. A controller trigger makes migration state observable through
the API and supports concurrent migrations, but requires building and operating the controller.

### Pre-commit validation

**Decision.** How is the v2 configuration's correctness checked before it takes over? Because the v2
composition becomes authoritative once promoted, most of this happens up front, before any live
change, and can run again right before authority transfers.

**Options.**
- **None.** Rely on the safety window (Observe or paused) to surface problems through resource status.
- **Render diff.** Run `crossplane render` and compare. The comparison base varies:
  - a v1 render against a v2 render (desired against desired)
  - a v2 render against the v1 actual MRs, with the mechanical translation applied to both sides so
    only structural divergence shows
  - a v2 render against live observed state (`crossplane render --observed-resources`), classifying each
    resource as adopt / net-new / orphan and gating on zero orphans.
- **Schema dry-run.** Validate the translated or rendered MRs against the v2 CRDs.
- **Drift check at promote.** Compare `spec.forProvider` against `status.atProvider` immediately before
  transferring authority, to catch cloud state that drifted during the window.
- **Review by an agent.** An LLM reviews the migration change against cluster state.

**Consequences.** `crossplane render` is an existing CLI primitive. The classification and gating on top
of it are not built in. Each comparison base catches different divergence: desired-against-desired
catches composition rewrites, desired-against-actual catches drift from the v1 baseline, and
desired-against-observed catches resources that would be created or orphaned against live cloud state.

### Window safety primitive

**Decision.** During the migration window, what prevents a controller from writing to the cloud
resource before authority is intended to transfer?

**Options.**
- **`managementPolicies: ["Observe"]`.** The MR reconciles read-only: the provider reads cloud state
  into `status` but performs no writes.
- **`crossplane.io/paused`.** The MR is not reconciled at all. It stays inert until the annotation is
  removed.
- **Pausing the v1 claim and composite.** Stops the v1 XR from reconciling and re-asserting state
  during the handoff.
- **Two control planes.** v2 runs on a separate cluster in a read-only mode while v1 remains
  authoritative on the original cluster.
- **SSA field-ownership timing.** Relies on the provider claiming ownership of the external-name within
  a specific window before an injected function is removed from the pipeline.

**Consequences.** Observe still reconciles (read-only) while paused does not reconcile at all. Both
prevent cloud writes. Pausing the v1 side and Observe/paused on the v2 side are complementary and
address the single-write-authority constraint from opposite ends. A second control plane avoids v1 and
v2 CRDs coexisting on one cluster, at the cost of running that extra plane. SSA field-ownership timing
depends on provider behavior and step ordering rather than an explicit read-only state.

### Matching

**Decision.** How is each v1 managed resource mapped to the v2 managed resource that should adopt its
cloud resource?

**Options.**
- **composition-resource-name (crn).** The annotation Crossplane sets per composed resource. Stable
  across v1 and v2 only if the composition keeps the same template or step names.
- **Explicit user-applied labels.** Label values (`resource/v1` and `resource/v2`) that the
  user adds to both the v1 and v2 compositions.
- **1:1 mechanical translation with provenance.** Each v1 MR maps to exactly one translated v2 MR, with
  the source name and owner recorded in annotations.
- **External cloud identity.** Match by the actual cloud resource ID rather than a Kubernetes-side key.

**Consequences.** crn and label matching both depend on the mapping key being held stable across v1 and
v2. A renamed template or step breaks crn matching unless a remap is supplied. Label matching requires
editing both compositions to carry the labels. Matching by cloud identity is independent of
Kubernetes-side naming but requires reading the cloud resource.

### Source of desired state and external-name placement

**Decision.** When a new v2 MR object is created (the namespaced option in the object-strategy axis), two things must be populated:
its `spec.forProvider`, and its external-name. This axis is moot for the reparent path, where the
object and its fields are preserved as-is.

**Options for `spec.forProvider`.**
- **v2 composition re-render.** The composition produces `spec.forProvider`, which is authoritative
  from creation.
- **Mechanical translation of the v1 spec.** The v1 MR's `spec.forProvider` is rewritten to v2 shape
  and written onto the v2 MR.
- **Minimal stub plus LateInitialize.** The MR is created with only the fields needed to identify the
  cloud resource. Observe and LateInitialize populate the rest from cloud state.

Per the composition-authority constraint, whatever populates the spec initially, the v2 composition
re-renders `spec.forProvider` once it has authority, so any initial values that differ from the
composition's output are transient.

**Options for external-name placement and ownership.**
- **Writer.** Who sets the external-name. Options: a migration tool at MR creation, an injected
  composition function reading a saved map (a ConfigMap), a composition function reading a data
  structure carried in the resource graph, an external script via `kubectl patch`, or an operator via
  `kubectl annotate`.
- **Field-manager ownership.** For the external-name to persist through promotion, it must be owned by
  a field manager that later reconciliation will not strip. It can be owned by the provider (after it
  reconciles and claims the annotation), by an out-of-band writer (the tool or `kubectl`), or written
  by the composition itself on every render. If the composition or a GitOps reconcile owns the field
  and omits it, it can be removed.

---

## How the axes constrain each other

This section states which combinations are technically forced or precluded.

- **Target scope forces object strategy.** Namespaced MRs require creating new MR objects and
  transferring external-name. Cluster-scoped MRs permit reparenting, which makes the external-name placement axis moot
  (the object and its external-name are preserved).
- **Trigger constrains logic location.** A GitOps-PR trigger composes cleanly with in-band
  logic or a controller. An out-of-band imperative mutation under GitOps requires explicit
  sequencing against the reconciler to avoid being reverted.
- **Safety primitive interacts with topology.** A second control plane removes the need for v1
  and v2 CRDs to coexist and makes read-only observation on v2 independent of v1. Single-cluster
  options rely on Observe or paused plus a different API group to let v1 and v2 coexist.
- **Matching interacts with validation.** crn or label matching presumes a stable mapping key.
  A render diff is what surfaces a broken or shifted mapping before commit.

---

## Cross-cutting requirements

These concerns cut across the axes. Any adoption design has to address them regardless of the choices
above.

- **Connection-secret handoff.** Adoption can recreate an MR's connection secret empty. Downstream
  consumers that read the secret (provider-kubernetes or provider-helm reading a kubeconfig
  secret) can lose connectivity until the producing resource reconciles again. v2 also changes how
  connection secrets are produced (composed explicitly rather than built into the XR), so the secret's
  name, namespace, and population all have to be preserved across the handoff.
- **Cross-namespace references.** Cluster-scoped v1 MRs can reference each other freely. Namespaced v2
  MRs resolve references only within their namespace, so a namespaced move must either co-locate
  referenced resources in one namespace or switch to ID-based references (a composition change).
- **Nested XRs.** Platforms that compose XRs from other XRs need a defined migration order across the
  levels.
- **Cloud API rate limiting.** While both v1 and v2 observe the same resources, cloud API calls for
  those resources roughly double for the duration of the window.
- **crn stability.** Matching by composition-resource-name assumes the crn is stable across v1 and v2.
  Legitimate renames or splits require a remap.
- **Manual-rewrite boundary.** Composition-function business logic cannot be transformed automatically.
  This is the boundary between the authoring half (out of scope here) and adoption: no adoption design
  removes the need for the user to rewrite function logic for v2.

---

## Open questions for the design effort

- Does the v2 composition reliably render and write `spec.forProvider` while its MRs are in
  Observe-only mode? Composition runs at the XR level and the management policy governs only the
  provider's cloud writes, which suggests yes, but is worth confirming since the design depends on it.
- What field-manager ownership model for external-name survives both a v2 composition re-render and a
  GitOps reconcile without the annotation being stripped?
- Where would adoption tooling live: the core Crossplane CLI, an in-cluster controller, or out of tree?
- How are connection secrets bridged when downstream consumers live in a different namespace and cannot
  be redeployed at the same instant?
- Does an observe or render parity check need a per-provider ignore list for non-deterministic fields
  (timestamps, list ordering, generated IDs)?
- How are MR kinds handled when a legitimate v1 → v2 change forces the composition-resource-name to
  change (resource splits, renames)?
