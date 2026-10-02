# 0022. Contracts are proto and Connect; Lambda RPCs are unary

- Status: accepted
- Date: 2026-10-02

## Context

The component's contracts are Protocol Buffers: the record
([0001](0001-record-schema-proto-with-json-schema-slots.md)), the sink, the
registry and the query API, served over Connect. Two things are new. The
parts may run on a function platform
([0016](0016-three-parts-installed-independently.md)), and the bucket now
carries payloads of its own that other people's tools must read
([0019](0019-seals.md)).

Streaming RPCs need a connection that outlives a request; an HTTP function
fronted by a gateway has none. And a consumer that does not speak Connect — a
dashboard, a script, a verifier in another language — wants a description in
a format its own tooling reads.

## Decision

**Proto is the source of every contract**: the RPCs, the seal, delegation and
revocation payloads, the configuration's shape where it is shared. Services
speak **Connect**.

**An RPC hosted on AWS Lambda is unary.** The platform's request and response
model has no stream, so a method served there is request and response only.
A streaming method is allowed where the host can hold a connection, and a
contract that must be reachable on both is written unary, with paging in the
request in place of a server stream.

**OpenAPI and JSON Schema are generated from the proto**, committed beside it
and checked for drift in CI, for consumers that do not use Connect. The
generated files are artefacts: an edit goes to the proto.

**Compatibility rules are those of
[0009](0009-versioning-policy.md)**: a package per major version, additive
changes within one.

## Consequences

- A function-hosted deployment loses nothing the contract needs, because the
  contract was never allowed to need a stream there.
- A consumer in any language has a schema it can generate from without
  adopting Connect.
- One more generated tree to keep in step, which the existing drift check
  already does for Go and JSON Schema.

## Alternatives considered

- **Hand-written OpenAPI.** It would drift from the proto the first time
  somebody edited one and not the other.
- **Streaming everywhere, with a fallback for functions.** Two shapes of each
  method to specify and test, for a gain only the long-lived hosts see.
- **gRPC only.** Not reachable from a browser or a function gateway without a
  translation layer, which is what Connect already is.
