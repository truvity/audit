-- Compare identifiers by their bytes, not by a language's rules.
--
-- Every text column here holds an opaque token: an action name, a tenant, an
-- actor identifier, a JSON pointer. None of them is prose, none is ever shown
-- to a reader in sorted order as words, and all of them are compared by two
-- other implementations of the same interface using Go's byte comparison.
--
-- Postgres, left alone, uses the database's collation. Under en_US.UTF-8 —
-- which is what an ordinary `createdb` gives you — punctuation is ignorable, so
-- 'svc-issuer' < 'svc.' is false. That one fact broke the prefix operator
-- silently: a prefix is compiled to the range [prefix, next(prefix)), and under
-- that collation the range excluded the very rows it was built around, so
-- `actor_id starts with "svc-"` returned nothing at all. The memory searcher and
-- the archive scan returned two rows for the same query. Nobody would have
-- found it by reading the SQL; the conformance suite found it on its first run.
--
-- The same reasoning applies past the prefix operator, which is why this is a
-- schema change and not a cast in one predicate. Keyset pagination resumes with
-- `column > boundary`, and ordering a page by action or tenant sorts on these
-- columns; under a linguistic collation both mean something subtly different
-- from what the other searchers mean, and "subtly different" in a cursor is how
-- a page silently loses a row.
--
-- C collation also lets the range scan use the ordinary btree index, which a
-- per-query COLLATE would not: an index built in one collation cannot answer a
-- comparison made in another.

-- The tenant policies have to come off first: a column named in a policy cannot
-- have its type altered while the policy stands. They are put back below,
-- unchanged, and they are the only thing standing between one customer and
-- another's trail — so this file recreates them rather than leaving that to a
-- later migration, and a failure here leaves the whole migration rolled back
-- with the policies intact.
drop policy if exists events_core_tenant on events_core;
drop policy if exists facet_counts_tenant on facet_counts;

alter table events_core
    alter column profile type text collate "C",
    alter column tenant_id type text collate "C",
    alter column source type text collate "C",
    alter column action type text collate "C",
    alter column operation type text collate "C",
    alter column outcome type text collate "C",
    alter column object_key type text collate "C";

alter table events_context
    alter column profile type text collate "C",
    alter column actor_kind type text collate "C",
    alter column actor_id type text collate "C",
    alter column subject_kind type text collate "C",
    alter column subject_id type text collate "C",
    alter column client_address type text collate "C",
    alter column request_id type text collate "C",
    alter column trace_id type text collate "C",
    alter column observer_id type text collate "C";

alter table events_data
    alter column profile type text collate "C",
    alter column path type text collate "C",
    alter column kind type text collate "C",
    alter column value_text type text collate "C";

alter table facet_counts
    alter column profile type text collate "C",
    alter column tenant_id type text collate "C",
    alter column field type text collate "C",
    alter column facet_value type text collate "C";

create policy events_core_tenant on events_core
    using (tenant_id = current_setting('audit.tenant_id', true)
        or current_setting('audit.tenant_id', true) is null
        or current_setting('audit.tenant_id', true) = '');

create policy facet_counts_tenant on facet_counts
    using (tenant_id = current_setting('audit.tenant_id', true)
        or current_setting('audit.tenant_id', true) is null
        or current_setting('audit.tenant_id', true) = '');

insert into audit_schema_version (version) values (3) on conflict do nothing;
