# 0018. The v1 bucket layout, and v0 is dropped

- Status: accepted; supersedes the object layout of [0003](0003-s3-object-lock-as-the-record.md)
- Date: 2026-10-02

Supersedes the object layout in
[0003](0003-s3-object-lock-as-the-record.md) (its `year=/month=/day=`
keys); the rest of 0003 stands. The layout itself is specified in
[the bucket contract](../reference/bucket-contract.md).

## Context

The v0 archive keys each object by profile, then tenant, then the
`year=/month=/day=` partitions of the day the record was written, and the
digest job lists a day at a time. Three things make that a poor contract for what comes
next.

**The key does not say when an object arrived.** A day is the record's
date, a writer rolls objects by size or time, and a reader that wants "what
is new" must list a whole day and compare. A follower that resumes from a
cursor needs keys that sort by the time the object *became visible*, which
is ingest time, not the record's own time.

**An object is not self-describing.** Its hash is computed by whoever reads
it, so sealing an hour means fetching every object. A notary that only lists
and reads metadata is cheaper by a factor of the object size and can run on
a function platform.

**The catalogue lives with the writer.** A record names the version it was
written under, and the archive keeps a copy under a prefix, written by the
writer on every start, which is a write the writer should not be repeating.

## Decision

**v1 keys are**

```text
records/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>/<ULID>
```

keyed by **ingest time** (UTC): the hour is the hour the batch was taken
and the ULID is generated when the put starts, so keys sort in the order
batches arrived. There is **one object per ingest batch**. Each record in
it carries its own **hash**, and the object's **sha256 is in its metadata**,
so a notary reads a listing and a `HEAD`, never a body.

**The profile stays the first key component.** A profile is the unit that
differs in retention, in what identities are kept or pseudonymised, and in
lifecycle: a bucket policy, a lifecycle rule, a retention default and a
replication rule are all written against a prefix, and the first component
is the one they are cheap to write against. Putting the tenant or the date
first would make every such rule a pattern over the middle of a key, which
S3 prefixes cannot express.

**`catalogue/<app>/<version>` is written once**, with a conditional put
(`If-None-Match: *`). The writer that finds it present compares content and
carries on, or refuses to start if the bytes differ for the same version.

Beside them: `seals/…` and `keys/…`
([0019](0019-seals.md)).

**There is no v0 compatibility.** No v1 component reads or writes the v0
layout, and nothing migrates it. The v0 archive is left where it is, locked,
until its retention allows it to be deleted; verification of it uses the last
release that knew the layout.

## Consequences

- **Following the archive is a listing with a cursor**
  ([0020](0020-observe-follows-the-bucket.md)), and sealing an hour is a
  listing and the metadata of its objects.
- **The writer's puts are bigger and fewer**, because a batch is one
  object regardless of its records' dates; the cost of that is that a
  record's own date no longer decides where it lives, and a reader finds a
  record by its ingest hour.
- **Per-record hashes** let a reader check one record without the batch's
  neighbours, and make the notary's tree something an auditor can recompute.
- **Two archives per deployment for the length of the old one's retention**,
  and no tool that spans them. That is accepted: the alternative was a
  compatibility layer in every part for a layout that was never meant to
  outlive it.
- A change to the layout is a new version of the contract with its own
  prefix, never an edit.

## Alternatives considered

- **Keep v0 and add metadata.** The key would still not sort by arrival, and
  every part would carry two layouts for ever.
- **Migrate the v0 archive.** Rewriting locked objects is impossible, and
  copying them breaks the digests that name them.
- **Date first, then profile.** Good for "everything of one day", and wrong
  for every rule that is per profile: lifecycle, retention and replication.
- **Tenant first.** The tenant is the application's own and carries no
  retention, so a rule against it says nothing a rule against the profile
  does not.
