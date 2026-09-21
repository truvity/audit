# Integrity

## Digest chain

Hourly, per profile, `audit digest` writes one digest covering every tenant's
objects written in the window:

```json
{
  "digest_version": "1",
  "profile": "security",
  "window_start": "2026-09-17T13:00:00Z",
  "window_end":   "2026-09-17T14:00:00Z",
  "objects": [
    { "key": "tenant=.../...ndjson.zst", "sha256": "…", "size": 123456,
      "retain_until": "2027-09-17T14:00:00Z" }
  ],
  "previous_digest_key": "digest/profile=security/.../13.json",
  "previous_digest_sha256": "…",
  "previous_digest_signature": "…",
  "signed_by": "arn:…:key/… | transit/keys/audit-digest",
  "signature": "…"
}
```

Empty windows produce a digest with no objects. The digest object is locked
with the same retention as the objects it covers.

## Why a job, and not a signature on each record

What the chain has to prove is that nothing was **removed**, nothing was
**changed**, and the operator did not **choose** what was signed. A
signature per record, made by the emitter or by the writer, proves only the
second:

- **Removal leaves nothing behind.** A signed record that is deleted takes
  its signature with it. Only a signed list of everything written in a
  window — including an empty list for a quiet hour — makes a missing object
  visible. That is what a digest is.
- **An emitter's signature says "the application said so",** which the trail
  already knows: the writer verifies each caller's workload token and stamps
  it as the record's observer, and the `origin_hash` covers the record as
  accepted. A compromised application would sign its own false records just
  as readily. Keys per emitter would also have to be issued, rotated and
  revoked in the least trusted place in the system, and every verifier would
  need all their public halves.
- **The writer must not hold the signing key.** It already has write rights on
  the archive; with the key as well, one compromised process could both write
  and vouch for what it wrote. The digest job runs as its own identity, may
  use the key, and may write only under `digest/`.
- **Quiet hours need a digest too,** and nothing wakes the writer when nothing
  happens. A scheduled job seals every hour, and one that missed its runs
  seals the hours it missed.

The cost is that the current hour is not signed yet. Until it is sealed, its
objects are protected by Object Lock in compliance mode — nobody, the
account's root included, can overwrite or delete them before their retention
ends — and not yet by the chain. A deployment that needs a shorter window runs
the job more often.

## Who holds the key

With a signing key the writer holds itself, the chain proves that objects have
not changed since signing, by a party who could also have chosen what to sign.
A managed key, in KMS or a transit engine, never leaves its provider, so the
chain also proves the operator did not choose what to sign. A deployment that
must answer an assessor uses a managed key; the local signer is for tests and
for a deployment that accepts the weaker claim knowingly.

## Verify

`audit verify --profile <p> --from <t> --to <t> [--public-key <pem>]`
walks the chain newest-first and prints per digest and per object `valid`,
`INVALID: hash mismatch`, `INVALID: signature`, `INVALID: missing`,
`INVALID: retention shorter than profile`, and a summary. Exit code is
non-zero on any invalid entry. Auditors run it with read-only credentials.

## Nightly

The nightly run verifies the previous day, records `audit.digest.verified`
or `audit.digest.failed`, and marks the windows in the index so the
[Audit page](audit-page.md) shows a badge.

### The job

`audit digest` resumes from the hour after the last digest, so a job that
missed its runs seals the windows it missed. That matters more than it sounds:
an hour with no digest cannot be told from one whose digest was removed, and
the verifier reports both the same way, so a gap left by a missed run is
evidence of tampering that nobody can resolve. It never seals the hour it wakes
in — objects are still being written into it — and it bounds one run to a week
of windows and reports how many are left. A window already sealed is left alone.

Objects are keyed by the day their records happened, and a record retried out
of an emitter's queue or redelivered by the stream is written under a day older
than the window it was written in, so both the builder and the verifier look
back seven days from the window. The verifier's lookback
wants to be at least the builder's, or an object the builder covered from
further back is not looked at.

The tenant sits between the profile and the date in the archive, so neither the
builder nor the verifier can ask for a profile's day as a prefix. Both ask for
the tenants first and walk one bounded listing per tenant and day
(`store.WalkDays`). Listing the whole profile would be right until the archive
outgrew a page and wrong in silence after.

Both jobs keep an account of themselves, given `--sink`: `audit.digest.written`
per sealed window, carrying how many objects it covers, and
`audit.digest.verified` or `audit.digest.failed` per window checked. Without
them a chain that was never sealed and one sealed over a quiet hour are
identical in the archive, and a verification that never ran looks exactly like
one that found nothing wrong. A window that could not be *written* gets no event
of its own: the catalogue's `failed` is about a verification, and the missing
window is what the next verification reports.

Sealing is a different privilege from writing. The signing key lives where the
writer's credentials do not, and the job runs as its own identity.

## Time

Emitters and writers run on synchronised clocks. `audit clock-sync` checks the
offset against UTC daily and records `audit.clock.synchronised`, which the
evidence and PCI presets require. It measures and records; it never sets the
clock, because a component that both set the time and recorded the times of
things would be marking its own paper.

The recorded `offset_ms` is the correction this clock needs, as RFC 5905 §8 has
it: positive means the clock is behind. Given several references it believes
the quickest to answer, since the error in an offset is bounded by half the
round trip that carried it. A clock outside the deployment's tolerance fails the
run and is recorded anyway — that hour is exactly the one an auditor wants the
measurement from. Nothing is recorded when no reference answered: the clock was
not checked, and saying it was would be worse than a failed job.

## Anchoring

Presets may require or recommend anchoring the chain head with an RFC 3161
or ETSI time-stamp. **Not built.** When it is, the token is kept beside the
digest (`<digest key>.tsr`), not inside it: a time-stamp is over the signed
digest, so it cannot be part of the body it signs. An earlier version of this
page said the digest carried an `anchor` field; it never did.

## Verification, per record

The nightly verification (`audit verify --record`, the chart's default) writes
one result per window under `verified/`, kept as long as the digest it checks.
`Get` reads a record's standing from there: the digest that names its object,
and when that digest was last verified **clean**. A later verification that
found a problem withdraws the answer, so `verified_at` is never a claim the
most recent check contradicts. A copy whose hour is not sealed yet has neither,
which is the ordinary state of the current hour.
