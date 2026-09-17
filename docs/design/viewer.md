# Viewer

One npm package, two surfaces.

- **Embedded**: headless hooks over the Connect-ES client plus a default
  MUI skin. The host passes a transport with its own bearer. No
  authentication inside the component.
- **Standalone console**: SPA plus a thin server, an OIDC relying party or
  behind a gateway.

## Navigation

Profiles are the top level. A user sees only the profiles their grant
allows. Inside a profile: tenant scope, time range with a histogram from
the counts table, a facet sidebar, a qualifier box (`actor:` `action:`
`target:` `outcome:` `tenant:` and a time range) that compiles to the
typed filter, and the reverse-chronological table.

## Row

A human sentence rendered from the catalogue template (ICU MessageFormat,
locale-aware), actor, target, outcome, time, and for the security profile
the client address. Expanding shows the JSON with filter-for and filter-out
on every value, the old-versus-new diff on updates, pivots by request id,
trace id, actor and target, a permalink by event id, and the integrity
badge when a verified digest covers the record.

## Actions

Live tail (polls the tail cursor), export (async job, signed URL), copy
permalink.

## Not Grafana

Grafana's Postgres datasource is right for operator dashboards over the
counts table. It cannot scope by the viewer's tenant claim, render
catalogue sentences, run export jobs or show integrity, so it complements
the console.
