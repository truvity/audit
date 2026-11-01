# audit

**This repository is retired and archived. The audit trail now lives in
[truvity/sluis](https://github.com/truvity/sluis), under
[`audit/`](https://github.com/truvity/sluis/tree/master/audit).**

The component moved there as a snapshot of v0.16.0, and every release since is
cut from that repository. Nothing about how it is consumed was renamed except
the Go module paths:

| What | Where it is now |
|---|---|
| Go modules `github.com/truvity/audit`, `.../sdk`, `.../deploy/pulumi` | `github.com/truvity/sluis/audit`, `.../audit/sdk`, `.../audit/deploy/pulumi` (rewrite the imports) |
| Images `ghcr.io/truvity/audit/*` | the same names, published by the truvity/sluis release |
| Helm chart `oci://ghcr.io/truvity/charts/audit` | the same name, published by the truvity/sluis release |
| CLI, Lambda zips and checksums | the truvity/sluis [releases](https://github.com/truvity/sluis/releases) |
| `@truvity/audit`, `@truvity/audit-react` | GitHub Packages, from truvity/sluis |
| Documentation | [`docs/guides/audit/`](https://github.com/truvity/sluis/tree/master/docs/guides/audit) and [`docs/concepts/audit/`](https://github.com/truvity/sluis/tree/master/docs/concepts/audit) in truvity/sluis |

Releases up to v0.16.0 and their packages stay readable here. The full history
of the component up to the move is in this repository.
