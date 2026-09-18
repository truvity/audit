-- The tenant policies take a grant's whole tenant list, and refuse by default.
--
-- The first policies compared tenant_id with one setting, audit.tenant_id, and
-- read an unset setting as "every tenant". Both were wrong for what the
-- policies are for. They are the second line under the service: the grant is
-- turned into a term of every query, and these hold when a new predicate, a
-- new code path or a facet query forgets that term. So:
--
-- A grant names several tenants as often as one, and a policy that can only
-- hold one would leave every multi-tenant reader with the first line alone.
--
-- And a connection that set nothing is exactly the mistake a second line exists
-- to catch. Reading "nothing set" as "everything allowed" made the policy open
-- precisely when it was forgotten. Now a reader that set nothing sees nothing,
-- and seeing every tenant is something only an explicit setting grants, which
-- only an operator's grant produces.
--
-- The list travels as a JSON array, not a delimited string: a tenant identifier
-- is free text and may contain any delimiter one could pick. The subquery is
-- uncorrelated, so it is planned once per statement rather than per row.
--
-- The owner still bypasses all of this. These bind a reader role that does not
-- own the tables, which is the role a reading service is meant to connect as.

drop policy if exists events_core_tenant on events_core;
drop policy if exists facet_counts_tenant on facet_counts;

create policy events_core_tenant on events_core
    using (current_setting('audit.all_tenants', true) = 'on'
        or tenant_id = any(array(
            select jsonb_array_elements_text(
                nullif(current_setting('audit.tenant_ids', true), '')::jsonb))));

create policy facet_counts_tenant on facet_counts
    using (current_setting('audit.all_tenants', true) = 'on'
        or tenant_id = any(array(
            select jsonb_array_elements_text(
                nullif(current_setting('audit.tenant_ids', true), '')::jsonb))));

insert into audit_schema_version (version) values (4) on conflict do nothing;
