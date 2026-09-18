-- Which key directory this deployment's writers share.
--
-- The local key provider mints random data keys and keeps them wrapped in a
-- directory. Two writers that do not share that directory mint different keys
-- for the same tenant, and a directory lost and recreated re-keys every tenant:
-- either way the same person silently gets a second pseudonym, and the trail
-- before stops linking to the trail after. Nothing inside one writer can see
-- it. This row can: the first writer registers its directory's identity, and a
-- writer that brings another is refused at start-up.
--
-- One row, enforced by the key. Replacing a directory on purpose — the old one
-- is gone and every pseudonym will change — is done by deleting the row, with
-- the runbook's reasons in mind, not by any writer deciding so on its own.
create table if not exists audit_key_directory (
    singleton     boolean     primary key default true check (singleton),
    directory_id  text        not null,
    registered_at timestamptz not null default now()
);

insert into audit_schema_version (version) values (5) on conflict do nothing;
