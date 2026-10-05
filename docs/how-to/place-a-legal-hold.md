# Place and release a legal hold

## Purpose

Keep objects under a prefix undeletable for as long as a matter lasts, whatever their
retention says.

## Preconditions

- The installation is on the **record tier** (Object Lock). On the attested tier
  `audit hold place` is refused and records the attempt.
- The operator's role has `s3:PutObjectLegalHold`, `s3:GetObjectLegalHold`,
  `s3:ListBucket` and `s3:PutObject` on `holds/`; releasing needs the break-glass role.
- A writer to record through (`--sink`).

## Before you start

- **A reason is required.** A hold nobody can account for cannot be safely released,
  because whoever finds it later has no way to know whether the matter is over.
- **A hold is one prefix**: `records/<profile>/` or, with `--tenant`,
  `records/<profile>/<tenant>/`, within one installation. It has no expiry of its own.
- **Releasing needs the break-glass role.** The bucket policy refuses
  `s3:PutObjectLegalHold` with `OFF` to everyone else, and the refused attempt is still
  recorded (`audit.hold.released`).
- **The record is made after the hold changes, not before**, so the trail never claims
  a hold that then failed. If the writer cannot take the record, the command fails
  saying the hold **is** placed (or released) and must be recorded by hand. The hold's
  own record under `holds/` is there either way.
- **There is a window.** `place` sweeps what is already there; the writer sets the hold
  on objects it writes afterwards, re-reading the active holds every minute. It reads
  them once before writing and refuses to start if it cannot; a refresh that fails
  keeps the last answer. The writer's role needs `s3:PutObjectLegalHold` (to set a
  hold on, never off) and read on `holds/`.
- **List holds before erasing a tenant's keys.**

## Steps

1. **List what exists.**

   ```
   audit hold list [--profile <p>] --bucket <b>
   ```

2. **Place it.**

   ```
   audit hold place --profile <p> [--tenant <t>] --reason <why> --by <who> --bucket <b> --sink <writer>
   ```

   Expected: every existing object under the prefix has a legal hold, `holds/<id>/placed.json`
   exists, and `audit.hold.placed` is recorded. Verify: `audit hold list`. Roll back:
   release it (step 3).

3. **Release it** (break-glass only).

   ```
   audit hold release --id <id> --by <who> --bucket <b> --sink <writer>
   ```

   Expected: `holds/<id>/released.json` and `audit.hold.released`. Roll back: place the
   hold again; it is a new hold with a new id.

## Afterwards

Tell the owner of the matter which hold ids exist. The holds are append-only records in
the archive, under the same lock as everything else.
