# Fix a writer that refuses to start on its key directory

## Purpose

Recover from `this writer's key directory is not the deployment's`.

## Preconditions

- The installation uses the `local` key provider (the transit provider has no
  directory, and none of this).
- Access to the pods' volumes and to the database.

## Before you start

- **The wrapped data keys are random, not derived, so the directory is the only copy.**
  Recreating it re-keys every tenant: the same person gets a new pseudonym and the trail
  before stops linking to the trail after.
- **A restored directory is accepted as it was**, because the identity is a file in it.
- **A deployment that has hit this is one to move to transit.**

## Steps

1. **Find which cause it is.**

   - **The directory is not shared.** With the `local` provider every replica must
     mount the same directory (`ReadWriteMany`). Fix the mount; nothing else.
   - **The directory was lost and recreated.** Restore it from backup if there is one.

   Verify: the writer starts. Roll back: none.

2. **Only if there is no backup and the new pseudonyms are accepted,** register the new
   directory by removing the old binding, and record why in the trail by hand:

   ```
   delete from audit_key_directory;
   ```

   The next writer to start registers its directory, and the rest must share it.
   Roll back: none; the old pseudonyms do not come back.

## Afterwards

Back up the key directory (the chart's `keysVolume`) and record the re-keying in the
deployment's own decision log.
