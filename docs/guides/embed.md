# Embedding the trail in your application

An application with no stream and no central writer to hand records to can
carry its own audit trail. It embeds the **writer**, so its records go from its
emitter straight into the locked bucket, in process. It can also serve the
**query API** behind its own sign-in, so its console reads the trail with the
same viewer a standalone deployment uses.

This is the same writer and query service the `audit-writer` and `audit-query`
binaries run: both binaries are built on these two packages. An embedded writer
keeps the same promises as a standalone one:

- nothing is acknowledged before it is in the archive;
- a record the writer cannot take is dead-lettered, never dropped;
- the writer records its own starts, stops and profile changes in the archive
  it writes;
- every read of the trail is itself recorded.

The complete example is [`examples/embed`](../../examples/embed/main.go). It
imports only public packages, and a test holds it to that, so it shows exactly
what a module outside this repository can do.

## The writer

```go
deployment, _ := preset.ParseDeployment(doc) // the document the chart renders
presets, _ := preset.Builtin()
profiles, _ := deployment.Compose(presets)

w, err := writer.Open(ctx, writer.Config{
	Archive:    archive,  // s3store on the Object-Locked bucket
	Profiles:   profiles,
	Keys:       provider, // keys.NewTransit, or keys.NewLocal
	Catalogues: []*catalogue.Catalogue{mine},
	Self:       "workload:my-app",
})
defer w.Close(ctx)

emitter, err := emit.New(emit.Options{Source: mine.Source, Catalogue: mine, Sink: w})
```

`writer.Open` refuses to start in the same situations the binary does:

- it cannot read the legal holds;
- it cannot record a change of profile;
- its database is at another schema version;
- several replicas would pseudonymise the same person differently.

`Self` is the observer stamped on records that arrive in process: your
application is the one emitting them. If you also serve the writer to other
processes (`w.Handler(authenticator)`), a record that arrives over HTTP carries
the caller the authenticator verified instead.

| setting | what it changes |
|---|---|
| `Database` (a `*pgxpool.Pool`) | the index, the deduplication table shared by replicas, and catalogues registered through the registry. Without it the writer indexes nothing and runs as one instance. |
| `Replicas` | above one needs `Database`, and keys every replica sees the same way: transit, or one shared `local` directory |
| `ForgetIdentities` | keeps no sealed identities, so nothing this writer writes can ever be resolved |
| `Logger`, `Meter` | where its logs and counters go; alert on `audit.writer.index.deferred` and `audit.writer.dead_lettered` ([runbook](../operations/runbook.md)) |

For keys in OpenBAO, the writer signs in with the pod's projected
service-account token. See [OpenBAO keys](../operations/openbao-keys.md).

## The query API

```go
q, err := query.New(query.Config{
	Searcher:      searcher,   // postgres.NewReader(pool), or &s3scan.Scanner{Store: archive}
	Authenticator: auth.AuthenticatorFunc(mySession),
	Authorizer:    grants,     // auth.Declarative, or the auth.AccessRoster preset
	Sink:          w,          // reads are recorded into the same writer
	Archive:       archive,    // Get's provenance; where resolve finds identities
	Keys:          sealer,     // optional: offers resolve
})
path, handler := q.Handler()
mux.Handle(path, handler)
```

The **authenticator** is where your application's own sign-in goes. It turns
the session it already verified into an `auth.Principal`: issuer, subject, and
the claims the grants match on. Build that Principal from what your application
verified, never from anything the caller merely sent. The grants and the record
of every read both rest on it.

The **searcher** decides what a search costs:

- `postgres.NewReader` over the index answers any query, but needs the index,
  which means giving the writer a `Database`. Connect it as a role that does
  not own the tables.
- `s3scan.Scanner` needs nothing but the bucket. It reads the archive within a
  budget and a horizon, and refuses what it cannot answer honestly: facets, and
  any ordering but time.

The service offers **resolve** only with `Keys`, and a caller still needs an
explicit `resolve` rule. It records the resolution before returning the
identity.

## Operations

Give the embedded writer what a standalone one needs:

- the Object-Locked bucket;
- IAM for `PutObject`, `PutObjectRetention`, `GetObjectRetention`,
  `PutObjectLegalHold`, and read on `holds/`;
- the keys.

Run the digest and verify jobs against the same bucket (`audit digest`,
`audit verify`; the chart's CronJobs can run them without the writer by
pointing them at your bucket). The chain covers every object your writer puts.
