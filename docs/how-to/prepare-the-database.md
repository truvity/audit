# Prepare the database

## Purpose

Have one Postgres database with an owner and a role for each part that uses it (the
writer, the indexer, the query service and, if you run it, the purge job), and the
schema applied.

## Preconditions

- A Postgres the installation can reach, in the application's existing cluster if it
  has one. A single instance is enough: the index is rebuildable from the archive, so it
  needs no backup and no replica.
- Another role to run the migration as: the **owner** of the tables.
- Not needed on AWS Lambda for the write path: the writer there has no database (its
  deduplication is DynamoDB). You still need one for observe and query.

## Before you start

- **The roles must be different.** The tenant row-level policies bind the query
  service's role; an owner bypasses them, so a part connecting as the owner would hold
  more than its own tables and every tenant's isolation would rest on the service alone.
  The chart refuses to render when the writer, the indexer or the query service names the
  owner's, or one another's, credentials.
- **The writer and the indexer never migrate themselves.** They refuse to start against a
  schema version they do not know, because several replicas would race.
- **A password inside a database URL is refused.** The URL has no password; the file
  names a secret (`passwordSecret`) that the `secrets` block resolves
  ([configuration](../reference/configuration.md#secrets)).
- **The index needs no backup.** `audit-observe` rebuilds it from the archive
  ([repair or rebuild the index](rebuild-the-index.md)).

## Steps

1. **Create the database and the roles.**

   ```sql
   create database audit;
   create role audit_owner login password :'owner';
   create role audit_writer login password :'writer';
   create role audit_observe login password :'observe';
   create role audit_query login password :'reader';
   grant all privileges on database audit to audit_owner;
   ```

   Verify: `\du` lists the four roles. Roll back: `drop database audit; drop role ...`.

2. **Apply the schema and grant each role what its part needs**, in one step.

   ```console
   $ audit migrate --database "$OWNER_URL" --writer audit_writer \
       --observe audit_observe --reader audit_query
   ```

   `--writer` gives the deduplication table, the registry and the key directory, and none
   of the index; `--observe` gives the index, its cursors and the one function that creates
   a month's partition; `--reader` gives select on the index and nothing else. Each is a
   key of the job's file as well; `--purge` names the purge role.

   Expected: it reports the schema version. Verify: the writer starts against the
   database. Roll back: none for the schema (it is additive); drop the database to start over.

3. **In the chart,** the same thing is the `migrate` hook (a pre-install and pre-upgrade
   Job whose config carries the roles); name each role's password secret in
   `database.passwordSecret` of its own component and project each from its Kubernetes
   Secret with `secretFiles`. With an operator that manages clusters declaratively, the
   database and users are in the cluster's own manifest.

## Afterwards

- A cluster of your own for the index is the exception: the index is a projection.
- Check that `audit conformance` passes against the query service once records land
  ([verify the trail](verify-the-trail.md)).
