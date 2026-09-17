-- The index is a projection. Everything here can be dropped and rebuilt from
-- the archive with `audit reindex`, and nothing here is evidence of anything.
-- What makes that safe is that the objects it is derived from are under an
-- object lock and accounted for by a signed digest chain.

create table if not exists audit_schema_version (
    version    integer     not null primary key,
    applied_at timestamptz not null default now()
);

-- events_core is what happened. It is partitioned by recorded_at because a
-- profile's retention expires by whole months, and dropping a partition is the
-- one way to forget a month that does not leave the table bloated.
--
-- The key includes recorded_at because a partitioned table's unique index must
-- contain the partition key. That is no weaker than (profile, id) in practice:
-- recorded_at is stamped into the copy the archive holds, so reindexing the
-- same object yields the same key, and two copies of one record with different
-- recorded_at can exist only where deduplication was bypassed, which the writer
-- refuses to allow.
create table if not exists events_core (
    profile      text        not null,
    id           uuid        not null,
    recorded_at  timestamptz not null,
    tenant_id    text        not null,
    occurred_at  timestamptz not null,
    seq          bigint      not null default 0,
    source       text        not null,
    action       text        not null,
    operation    text        not null,
    outcome      text        not null,
    target_types text[]      not null default '{}',
    target_ids   text[]      not null default '{}',
    object_key   text        not null,
    line         integer     not null,
    primary key (profile, id, recorded_at)
) partition by range (recorded_at);

-- The tail advances on recorded order, so that is the order this index is in.
create index if not exists events_core_tail
    on events_core (profile, tenant_id, recorded_at, seq, id);
create index if not exists events_core_action
    on events_core (profile, tenant_id, action, recorded_at desc);
create index if not exists events_core_time
    on events_core using brin (recorded_at);
create index if not exists events_core_targets
    on events_core using gin (target_ids);

-- events_context is who it happened to. It is kept apart so that a deployment
-- can purge it on a shorter schedule than the event, and can grant a reader the
-- event without the person.
--
-- client_address is text, not inet: one malformed address from one emitter must
-- not fail the batch that carries it, and the trail records what was reported.
create table if not exists events_context (
    profile        text        not null,
    id             uuid        not null,
    recorded_at    timestamptz not null,
    actor_kind     text        not null default '',
    actor_id       text        not null default '',
    subject_kind   text        not null default '',
    subject_id     text        not null default '',
    client_address text        not null default '',
    request_id     text        not null default '',
    trace_id       text        not null default '',
    observer_id    text        not null default '',
    primary key (profile, id, recorded_at)
) partition by range (recorded_at);

create index if not exists events_context_actor
    on events_context (profile, actor_id, recorded_at desc);
create index if not exists events_context_subject
    on events_context (profile, subject_id, recorded_at desc);

-- events_data is the extension properties an action's schema marked filterable.
-- Nothing else from the data slot is here: what a query may name is the
-- catalogue's decision, not the query writer's.
create table if not exists events_data (
    profile     text        not null,
    id          uuid        not null,
    recorded_at timestamptz not null,
    path        text        not null,
    kind        text        not null,
    value_text  text,
    value_int   bigint,
    value_time  timestamptz,
    primary key (profile, id, recorded_at, path)
) partition by range (recorded_at);

create index if not exists events_data_text
    on events_data (profile, path, value_text);
create index if not exists events_data_int
    on events_data (profile, path, value_int);

-- facet_counts is what the viewer's navigation reads. It is additive, which is
-- why counting is not a call of its own: only the transaction that inserted a
-- row knows whether it was new.
create table if not exists facet_counts (
    profile     text        not null,
    tenant_id   text        not null,
    hour        timestamptz not null,
    field       text        not null,
    facet_value text        not null,
    total       bigint      not null default 0,
    primary key (profile, tenant_id, hour, field, facet_value)
);

create index if not exists facet_counts_field
    on facet_counts (profile, tenant_id, field, hour desc);

-- seen is the shared deduplication table. With it, several writer replicas
-- agree about what has already been written; without it a deployment may run
-- exactly one, and the writer refuses to start otherwise.
-- id is text, not uuid: a malformed identifier from one emitter must not fail
-- the statement that carries a whole batch. The table is not evidence and its
-- size is bounded by the deduplication window, so the wider column is cheap.
create table if not exists seen (
    id      text        not null primary key,
    seen_at timestamptz not null default now()
);

create index if not exists seen_at on seen (seen_at);

-- Row-level security by tenant, read from a per-request setting. It binds only
-- roles that do not bypass it: the writer owns these tables and writes every
-- tenant's rows, and a reader role is granted select and gets one tenant.
-- A deployment that wants the owner bound too alters the tables to force it.
alter table events_core    enable row level security;
alter table events_context enable row level security;
alter table facet_counts   enable row level security;

drop policy if exists events_core_tenant on events_core;
create policy events_core_tenant on events_core
    using (tenant_id = current_setting('audit.tenant_id', true)
        or current_setting('audit.tenant_id', true) is null
        or current_setting('audit.tenant_id', true) = '');

drop policy if exists facet_counts_tenant on facet_counts;
create policy facet_counts_tenant on facet_counts
    using (tenant_id = current_setting('audit.tenant_id', true)
        or current_setting('audit.tenant_id', true) is null
        or current_setting('audit.tenant_id', true) = '');

-- events_context carries no tenant of its own; it is reached through the event,
-- so its policy is the absence of one and a deployment grants it separately.
drop policy if exists events_context_all on events_context;
create policy events_context_all on events_context using (true);

insert into audit_schema_version (version) values (1) on conflict do nothing;
