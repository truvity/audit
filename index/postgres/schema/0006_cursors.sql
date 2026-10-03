-- Observe follows the bucket by cursor (docs/decisions/0020), and the writer
-- stops indexing.
--
-- index_cursor is where each (profile, tenant) has been read to: the key of the
-- last object whose rows are in the index. It moves in the same transaction as
-- those rows, so a crash leaves either both or neither. It is a projection like
-- everything else here: dropping a row makes observe read that prefix again from
-- the start, and indexing is idempotent, so the cost is time and nothing else.
create table if not exists index_cursor (
    profile    text        not null collate "C",
    tenant_id  text        not null collate "C",
    last_key   text        not null collate "C",
    updated_at timestamptz not null default now(),
    primary key (profile, tenant_id)
);

-- Monthly partitions are made by observe, which is no longer the owner of the
-- tables, and creating a partition takes the owner. This function is the one
-- thing observe is allowed to do as the owner: it creates the three partitions
-- of a month and nothing else. It is SECURITY DEFINER with a fixed search path,
-- so the schema it acts on is the one it was created in and not the caller's.
do $$
begin
    execute format($f$
        create or replace function audit_ensure_month(month date) returns void
        language plpgsql security definer set search_path = %I, pg_catalog
        as $body$
        declare
            first_day date := date_trunc('month', month)::date;
            suffix    text := to_char(first_day, 'YYYY_MM');
            t         text;
        begin
            foreach t in array array['events_core', 'events_context', 'events_data'] loop
                execute format(
                    'create table if not exists %%I partition of %%I for values from (%%L) to (%%L)',
                    t || '_' || suffix, t, first_day, (first_day + interval '1 month')::date);
            end loop;
        end
        $body$
    $f$, current_schema());
end
$$;

-- Nobody but a role that was granted it may create partitions.
revoke all on function audit_ensure_month(date) from public;

insert into audit_schema_version (version) values (6) on conflict do nothing;
