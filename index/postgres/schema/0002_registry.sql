-- The catalogue registry. It shares this database and this migration chain
-- rather than keeping one of its own: a deployment that had to run two
-- migrations in the right order would eventually run them in the wrong one, and
-- the writer refuses to start on a version it does not know precisely so that
-- no service migrates itself.

create table if not exists catalogues (
    source        text        not null,
    version       text        not null,
    document      bytea       not null,
    -- The extension schemas the document references, keyed by their $id.
    schemas       jsonb       not null default '{}',
    registered_at timestamptz not null default now(),
    registered_by text        not null default '',
    primary key (source, version)
);

create index if not exists catalogues_source on catalogues (source, registered_at desc);

insert into audit_schema_version (version) values (2) on conflict do nothing;
