# 0014. The audit front, its console page and `audit verify`

Status: accepted, 2026-11-21

## Context

People have to read the log through something that faces them. The piece that holds the index credential must not parse internet input. A product that does not use sluis needs a way to look at its audit. A console that an identity product already runs should be able to show the audit without a second console. And an auditor must never be locked out by the front or by the issuer: verification has to work from a bucket alone.

## Decision

### The audit repository owns the front, the page and the command

- **audit owns three things:** the front, the console page, and `audit verify` (with `audit read`).
- **sluis's console embeds the page.** sluis owns the way it embeds, and audit owns the page. The console of another product may embed it too.

### The front

- **An audit has a front of its own, also when it runs beside an identity product.** The front is the only edge listener of the audit. It parses, validates against closed lists generated from the audit contracts and routes to the reader. It holds no key, no secret and no state, and has no outbound network. The reader is never the edge itself.
- **The edge connector is the shape's own:** a tunnel to a Unix-socket origin on a host, the Lambda Web Adapter on Lambda, a key-less relay container on Kubernetes.
- **The front has a host of its own,** a sibling of the identity product's host and never a child of it or a path on it. A sibling inherits no cookie scope. A passkey verifier's allow-list names it.
- **Open: the front's executable.** In sluis the front is the same `entry` executable as the identity product's, with the route tables of the audit contracts. Whether audit reuses that executable, or builds its own thin front, is not decided. The rules above hold for either.

### The console page

- **The console page is a static bundle,** built on audit's release train, served same-origin with the front, with only audit pages and a tight content security policy. It works with no identity product beside it, and a console of another product can embed it.
- **It offers** search with facets, records with their proofs and verification status, exports, and the health of each project's logs: the age of the latest checkpoint, the witness cosignatures and the index lag.
- **Reads go straight to the reader.** A write, such as an index rebuild, goes through a console component that checks the operator's delegation and routes the write to the piece that owns it. The reader stays read-only.
- **The browser holds no token.** The page's session is an HttpOnly cookie held by a session component. Where the page is embedded in another console, the console reaches the front with an exchanged token for the audit client, valid for at most five minutes and bound to the console's workload certificate by mutual TLS. The person's identity reaches the reader, which decides ([0013](0013-access.md)).
- **Who sees what follows the bindings** of [0013](0013-access.md).

### `audit verify`

- **`audit verify` and `audit read` work from a project's bucket alone,** with read-only credentials, and with no front, reader or sign-in. A record decrypts only for a caller that the `seal` block lets open it. Verification needs no key at all. Auditors and incident responders are never locked out by the front or the issuer.
- **`audit verify` checks a log** as [0009](0009-witnesses-and-verification.md) says, and reports a closed reason for each refusal ([reference/audit-verify.md](../reference/audit-verify.md)). It takes the keys the auditor pinned.
- **`audit search` builds a local index from the log** ([0012](0012-search-and-the-reader-api.md)).

### Built and planned

Built: `verify` over a directory with one log key and the checkpoint history check (sluis's CLI as `sluis audit verify`). Planned: the front in audit, the console page and its embedding, the bucket check, witnesses, several keys, `audit read` and `audit search`, and the rename of the command.

## Consequences

- audit can be used without a console of an identity product, and an identity product's console gains an audit page without owning it.
- sluis's console depends on an audit package, with its version.
- The front is one more edge listener and one more host, with its own certificate and allow-list entries.
- Reading the log through the front depends on the issuer. A read from the bucket is the way around it, so bucket read access is granted carefully, per project and per prefix.

## Roads not taken

- **The page and the command stay in the identity product.** audit could not be used without that product's console.
- **A remote plugin call from a console instead of a front of its own,** or the reader facing the edge. The piece holding the index credential would parse internet input, and an audit would be unreadable without a console.
- **One front for both contract sets when an identity product and audit run together.** One listener fewer, but that front parses both contract sets, and the audit front would differ between shapes.
- **A second console bundle per product.** The page is one bundle that others embed.
