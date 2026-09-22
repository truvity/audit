# Which presets a deployment composes

This repository ships seven [framework presets](../../presets/README.md). A
deployment does not compose all of them. This page says which ones an
installation is expected to turn on, which are kept for a contract that asks,
and what each one costs to run.

A preset nobody composes costs nothing: it is a file in the binary. A preset
a profile composes costs storage for the years it demands, and whatever
controls it requires — a daily clock-synchronisation job, a legal-hold
procedure, a review cadence somebody performs.

## The seven, side by side

| preset | what it keeps | retention | identities | demands | compose it |
|---|---|---|---|---|---|
| `security` | authentication, authorisation, privileged access, configuration change, key and secret use, every read of the trail | 365 days, 90 hot (minimum 180) | staff clear, external pseudonym | daily clock-synchronisation event, digest chain, compliance lock, reads logged | **always** |
| `billing-nl` | quantities per tenant and meter; no actor, no subject | 7 years | both omitted | digest chain, compliance lock | **when the installation meters** |
| `history` | what a tenant's own administrator changed, as sentences | 365 days | staff by role (see below), tenant's own people scoped | nothing beyond the chain | when a product shows activity to its tenants |
| `evidence-etsi` | credential and trust-service lifecycle facts | 7 years after the credential expires (10-year fallback) | staff clear, external pseudonym | daily clock-synchronisation, timestamp anchor recommended, quarterly verification report | a trust-service deployment under audit |
| `pci-dss` | the security set, at PCI's floor | 12 months, 3 hot (minimum 365 days) | staff clear, external pseudonym | **daily** review with dispositions | a deployment in cardholder-data scope |
| `dora` | the security set, plus logging-failure detection | 365 days, entity-defined | staff clear, external pseudonym | documented reference time source, monthly review | a supplier to a financial entity, when the contract flows it down |
| `nen-7513` | every access to a person's care record, with role, subject, on-behalf-of and reason | 5 years | staff clear, external scoped | monthly review, overview on a person's request | a healthcare deployment |

## The policy

**Compose `security` in every installation.** It is what makes the trail a
security record rather than a log, and everything the component does to
itself — every read of the trail, every digest, every job — is in it. Note
what it demands: `clock_sync_event: daily`. The chart refuses to render an
installation that composes `security` without a reference clock configured
for the clock-synchronisation job, because an integrity chain whose
timestamps nobody vouches for proves less than it appears to.

**Compose `billing-nl` where the installation meters.** It is the profile
that makes a billable quantity keepable for the seven years a tax authority
may ask about it, and it keeps only quantities: no actor, no subject, nothing
about a person. It is not a security profile and does not replace one — a
metering installation composes both, and the same record lands in two copies
with two retentions.

**`history` omits staff.** A tenant's administrator sees what happened to
their tenant, and who of their own did it; the operator's staff appear by
kind and role and never by identity, which is what
[0013](../decisions/0013-no-pseudonymisation-keys-by-default.md) decided so
that a tenant-facing profile needs no key provider. Compose it where a
tenant reads their own history, and nowhere else.

**The other four stay as files.** `evidence-etsi`, `pci-dss`, `dora` and
`nen-7513` each exist for a deployment whose contract or regulator demands
it. None is composed by any deployment now, and none should be composed
speculatively: each raises retention, and one of them (`pci-dss`) obliges
somebody to perform a daily review and keep the dispositions. Compose one
when the obligation is real, and record that decision where the deployment's
other decisions live.

## What composing a second preset does

Composition is a union, and it only ever tightens: the longest retention
wins, the stricter identity treatment wins, a forbidden field beats an
optional one, the most frequent review cadence wins. The rules are in
[the presets reference](../reference/presets.md), and
`audit profile explain <name>` prints what a profile actually keeps after
composition — including the effect of `external_identifiers_are_opaque`,
which can relax a preset's `pseudonym` to `clear` when the deployment has
declared that its external identifiers carry nothing direct.

Two consequences worth knowing before composing:

- **Retention cannot be shortened later.** Object Lock in compliance mode
  means an object written under a seven-year profile is there for seven
  years, whatever the profile says afterwards. Compose the long presets when
  the obligation exists, not in advance.
- **Some presets demand the lock and some do not.** `pci-dss`, `nen-7513`,
  `dora` and `evidence-etsi` demand Object Lock in compliance mode, so an
  installation composing any of them needs a bucket that has it; `security`,
  `history` and `billing-nl` are satisfied by the digest chain under a
  managed key, and may run on any S3-compatible store with `lockMode: none`.
  A component refuses to start when the store is weaker than a composed
  profile demands
  ([0014](../decisions/0014-lock-modes-and-store-tiers.md)).
- **A profile that requires a category nobody emits refuses to start.** The
  writer checks the registered catalogue against the profile's required
  categories, so composing `nen-7513` in an installation whose application
  records no patient-record access is caught at start-up, not at audit time.
