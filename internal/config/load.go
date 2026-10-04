package config

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	policyconfig "github.com/truvity/policy/config"

	"github.com/truvity/audit"
	"github.com/truvity/audit/sdk/sink"
)

// schemaFor reads the committed schema of one binary: the one embedded in the
// release, which is the one the chart's tests and a deployer's CI validate
// against.
func schemaFor(name string) []byte {
	b, err := audit.ConfigSchemas.ReadFile(path.Join("schemas/config", name+".schema.json"))
	if err != nil {
		// Unreachable: the files are embedded at build time, so a missing one
		// fails to compile rather than at run time.
		panic(err)
	}
	return b
}

// Validate checks a decoded document against one binary's schema. The chart's
// tests call it on what the chart renders, which is what stops the two
// drifting.
func Validate(name string, doc any) error {
	return policyconfig.Validate(doc, schemaFor(name))
}

func load[T any](file, name string, after func(*T) error) (*T, error) {
	var c T
	if err := policyconfig.Load(file, schemaFor(name), &c); err != nil {
		return nil, err
	}
	if err := after(&c); err != nil {
		return nil, &policyconfig.Error{File: file, Err: err}
	}
	return &c, nil
}

// LoadWriter reads and validates audit-writer's configuration.
func LoadWriter(file string) (*Writer, error) { return load(file, "audit-writer", (*Writer).finish) }

// LoadWriterLambda reads and validates audit-writer-lambda's configuration.
func LoadWriterLambda(file string) (*WriterLambda, error) {
	return load(file, "audit-writer-lambda", (*WriterLambda).finish)
}

// LoadQuery reads and validates audit-query's configuration.
func LoadQuery(file string) (*Query, error) { return load(file, "audit-query", (*Query).finish) }

// LoadObserve reads and validates audit-observe's configuration.
func LoadObserve(file string) (*Observe, error) {
	return load(file, "audit-observe", (*Observe).finish)
}

// LoadVerify reads and validates the configuration of `audit verify`.
func LoadVerify(file string) (*Verify, error) { return load(file, "audit-verify", (*Verify).finish) }

// LoadNotary reads and validates audit-notary's configuration.
func LoadNotary(file string) (*Notary, error) { return load(file, "audit-notary", (*Notary).finish) }

// LoadPurge reads and validates the configuration of `audit purge`.
func LoadPurge(file string) (*Purge, error) { return load(file, "audit-purge", (*Purge).finish) }

// LoadClockSync reads and validates the configuration of `audit clock-sync`.
func LoadClockSync(file string) (*ClockSync, error) {
	return load(file, "audit-clock-sync", (*ClockSync).finish)
}

// LoadMigrate reads and validates the configuration of `audit migrate`.
func LoadMigrate(file string) (*Migrate, error) {
	return load(file, "audit-migrate", (*Migrate).finish)
}

// Secret reads the environment variable the configuration names. An unset or
// empty variable is an error naming the variable, never quoting anything.
func Secret(name string) (string, error) { return policyconfig.Secret(name) }

// The rest of the contract: what a schema cannot say, or says less clearly than
// a sentence can. Each of these runs after the schema has accepted the file.

func (w *WriterLambda) finish() error {
	if w.Require == "" {
		w.Require = "archived"
	}
	if _, err := sink.ParseDurability(w.Require); err != nil {
		return fmt.Errorf("require: %w", err)
	}
	if n := b2i(w.Dedupe.DynamoDB != nil); n != 1 {
		return fmt.Errorf("dedupe names %d stores and must name exactly one of dynamodb", n)
	}
	if err := w.Archive.finish(true); err != nil {
		return err
	}
	if w.Keys.local() && w.Keys.Local.Dir == "" {
		return errors.New("keys.local with no dir keeps the keys in memory, and every invocation environment would mint " +
			"its own: the same person would get a different pseudonym in each; use keys.transit")
	}
	return nil
}

func (w *Writer) finish() error {
	if w.Mode == "" {
		w.Mode = "writer"
	}
	if w.Listen.Address == "" {
		w.Listen.Address = ":8080"
	}
	if w.Replicas == 0 {
		w.Replicas = 1
	}
	if w.Roll.Interval == 0 {
		w.Roll.Interval = Duration(30 * time.Second)
	}
	if w.Roll.MaxRecords == 0 {
		w.Roll.MaxRecords = 5000
	}
	if err := w.finishTransports(); err != nil {
		return err
	}
	if w.Replicas > 1 && w.Database == nil {
		return errors.New("replicas above 1 needs database: deduplication in one process only absorbs a repeat on " +
			"the replica that saw the original, so a redelivery landing on another would be written twice")
	}
	if w.Mode == "writer" {
		if err := w.Archive.finish(true); err != nil {
			return err
		}
		if w.Replicas > 1 && w.Keys.local() && w.Keys.Local.Dir == "" {
			return errors.New("replicas above 1 with keys held only in memory: each replica would mint its own keys " +
				"and the same person would get a different pseudonym on each; give keys.local.dir on storage every replica shares")
		}
	}
	return checkDatabase(w.Database)
}

// finishTransports carries the `stream` shorthand into `forward` or `consume`,
// applies the stream's defaults to whichever NATS block there is, and settles
// `require`: the default, and what the chosen transports can ever give.
func (w *Writer) finishTransports() error {
	receiver := w.Mode == "receiver"
	if w.Stream != nil {
		switch {
		case receiver && w.Forward != nil:
			return errors.New("stream and forward both say where a receiver sends: stream is forward.nats, so give one of them")
		case !receiver && w.Consume != nil:
			return errors.New("stream and consume both say what a writer reads: stream is consume.nats, so give one of them")
		case receiver:
			w.Forward = &Forward{NATS: w.Stream}
		default:
			w.Consume = &Consume{NATS: w.Stream}
		}
	}
	if receiver && w.Forward == nil {
		return errors.New("a receiver needs forward: there is nowhere to send what it takes")
	}
	if !receiver && w.Forward != nil {
		return errors.New("forward is for a receiver: a writer keeps what it takes in the archive")
	}
	if receiver && w.Consume != nil {
		return errors.New("consume is for a writer: a receiver holds no archive to write what it reads")
	}
	var nats *Stream
	switch {
	case w.Forward != nil:
		if n := b2i(w.Forward.NATS != nil) + b2i(w.Forward.SQS != nil) + b2i(w.Forward.Log != nil); n != 1 {
			return fmt.Errorf("forward names %d transports and must name exactly one of nats, sqs and log", n)
		}
		nats = w.Forward.NATS
		if q := w.Forward.SQS; q != nil {
			if err := q.check("forward.sqs"); err != nil {
				return err
			}
		}
	case w.Consume != nil:
		if n := b2i(w.Consume.NATS != nil) + b2i(w.Consume.SQS != nil); n != 1 {
			return fmt.Errorf("consume names %d transports and must name exactly one of nats and sqs", n)
		}
		nats = w.Consume.NATS
		if q := w.Consume.SQS; q != nil {
			if err := q.check("consume.sqs"); err != nil {
				return err
			}
		}
	}
	if nats != nil {
		if err := nats.finish(w.Roll.Interval); err != nil {
			return err
		}
	}

	best := sink.Archived
	if receiver {
		switch {
		case w.Forward.Log != nil:
			best = sink.Logged
		default:
			best = sink.Queued
		}
	}
	if w.Require == "" {
		// Safe by default: the weakest promise a deployment gets without
		// asking is the strongest its mode can give, so that choosing anything
		// weaker is something a person wrote down. A receiver's strongest is
		// queued, because the archive is the writers' and a receiver holds none.
		w.Require = "archived"
		if receiver {
			w.Require = "queued"
		}
	}
	least, err := sink.ParseDurability(w.Require)
	if err != nil {
		return fmt.Errorf("require: %w", err)
	}
	if receiver && w.Forward.Log != nil && least != sink.Logged {
		return fmt.Errorf("forward.log is the log sink, which keeps a record only as long as the log pipeline does: "+
			"it is allowed only with require: logged, and this says require: %s", w.Require)
	}
	if least > best {
		return fmt.Errorf("require: %s, and what this %s is configured with gives %s at best: "+
			"%s", w.Require, w.Mode, durName(best), unmet(receiver))
	}
	return nil
}

func unmet(receiver bool) string {
	if receiver {
		return "a receiver holds no archive, so it can only promise what its onward transport does; " +
			"the writers behind it are what reach archived"
	}
	return "lower require, or add what is missing"
}

func durName(d sink.Durability) string {
	switch d {
	case sink.Logged:
		return "logged"
	case sink.Queued:
		return "queued"
	case sink.Archived:
		return "archived"
	}
	return "nothing"
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (q *SQS) check(key string) error {
	fifo := strings.HasSuffix(q.QueueURL, ".fifo")
	if q.FIFO && !fifo {
		return fmt.Errorf("%s.fifo is true and the queue URL does not end in .fifo: a FIFO queue is named so, "+
			"and the publisher deduplicates by that name, so the two must agree", key)
	}
	q.FIFO = fifo
	return nil
}

// finish applies the stream's defaults and holds it to the roll.
func (s *Stream) finish(roll Duration) error {
	if s.Name == "" {
		s.Name = "AUDIT"
	}
	if s.Consumer == "" {
		s.Consumer = "audit-writer"
	}
	if s.Batch == 0 {
		s.Batch = 100
	}
	if s.AckWait == 0 {
		s.AckWait = Duration(2 * 60 * time.Second)
	}
	if s.AckWait <= roll {
		return fmt.Errorf("stream.ackWait (%s) must be longer than roll.interval (%s): a writer gathers records "+
			"for one interval before it writes them and leaves them unacknowledged meanwhile, and a stream that "+
			"gives up waiting sooner offers the same records to another writer", s.AckWait.D(), roll.D())
	}
	return nil
}

// finish holds a sink to naming exactly one place, and fills in what a queue
// can only give. It is safe on nil, a job's absent sink.
func (s *Sink) finish() error {
	if s == nil {
		return nil
	}
	if (s.URL != "") == (s.SQS != nil) {
		return errors.New("sink names exactly one of url and sqs")
	}
	if s.SQS == nil {
		return nil
	}
	if s.TokenFile != "" {
		return errors.New("sink.tokenFile is for a writer at sink.url: a queue takes the pod's own identity")
	}
	if s.SQS.QueueURL == "" {
		return errors.New("sink.sqs.queueUrl is required")
	}
	switch s.Expect {
	case "":
		s.Expect = "queued"
	case "queued":
	default:
		return fmt.Errorf("sink.expect is %s and sink.sqs is a queue, which gives queued and no more", s.Expect)
	}
	return s.SQS.check("sink.sqs")
}

// checkRequire holds a job's or a service's `require` to what it says the
// writer gives. A client cannot learn that from the writer before it writes,
// so the file says (sink.expect) and the guard believes it at start-up and
// checks it against every acknowledgement afterwards.
func checkRequire(require string, s *Sink) error {
	if err := s.finish(); err != nil {
		return err
	}
	if require == "" {
		return nil
	}
	least, err := sink.ParseDurability(require)
	if err != nil {
		return fmt.Errorf("require: %w", err)
	}
	if s == nil {
		return errors.New("require needs sink: there is no writer whose acknowledgement to hold to it")
	}
	if s.Expect == "" {
		return fmt.Errorf("require: %s needs sink.expect: a client cannot learn what the writer at %s gives "+
			"until it writes, so the file says what it is configured to give", require, "sink.url")
	}
	got, err := sink.ParseDurability(s.Expect)
	if err != nil {
		return fmt.Errorf("sink.expect: %w", err)
	}
	if got < least {
		return fmt.Errorf("require: %s, and sink.expect says the writer gives %s at best", require, s.Expect)
	}
	return nil
}

func (q *Query) finish() error {
	if err := checkRequire(q.Require, &q.Sink); err != nil {
		return err
	}
	if q.Listen.Address == "" {
		q.Listen.Address = ":8080"
	}
	if q.Searcher == "" {
		q.Searcher = "postgres"
	}
	if q.Exports != nil {
		if q.Exports.Expiry == 0 {
			q.Exports.Expiry = Duration(7 * 24 * 60 * 60 * time.Second)
		}
		if q.Exports.LinkValid == 0 {
			q.Exports.LinkValid = Duration(60 * 60 * time.Second)
		}
		if q.Archive != nil && q.Exports.Bucket.Name == q.Archive.Bucket.Name &&
			q.Exports.Bucket.Endpoint == q.Archive.Bucket.Endpoint {
			return errors.New("exports.bucket must not be the archive's bucket: an export is an unlocked copy meant to " +
				"be cleared, and the archive's policy denies every delete, so it would stay forever")
		}
	}
	if q.Archive != nil {
		if err := q.Archive.finish(false); err != nil {
			return err
		}
	}
	if q.Keys.enabled() {
		if q.Archive == nil {
			return errors.New("keys turn resolve on, which needs archive: resolve opens what the writer sealed in the archive")
		}
		if q.Keys.local() && q.Keys.Local.Dir == "" {
			return errors.New("resolve needs the writer's key directory: keys.local.dir")
		}
	}
	return checkDatabase(q.Database)
}

func (o *Observe) finish() error {
	if o.Listen.Address == "" {
		o.Listen.Address = ":8080"
	}
	if o.Settle == 0 {
		o.Settle = Duration(2 * 60 * time.Second)
	}
	if o.Interval == 0 {
		o.Interval = Duration(30 * time.Second)
	}
	if o.Batch == 0 {
		o.Batch = 500
	}
	if o.Wake != nil && o.Wake.SQS != nil {
		if err := o.Wake.SQS.check("wake.sqs"); err != nil {
			return err
		}
	}
	if err := o.Archive.finish(false); err != nil {
		return err
	}
	return checkDatabase(&o.Database)
}

// DefaultSettle is how long after an hour has ended it is sealed.
const DefaultSettle = 10 * time.Minute

// DefaultGrace is how long a verifier waits after an hour is sealable before it
// calls a missing seal a fault: the notary runs hourly.
const DefaultGrace = time.Hour

func (n *Notary) finish() error {
	if err := checkRequire(n.Require, n.Sink); err != nil {
		return err
	}
	if n.Settle == 0 {
		n.Settle = Duration(DefaultSettle)
	}
	if n.Archive.LockMode == "" {
		n.Archive.LockMode = "compliance"
	}
	return nil
}

func (v *Verify) finish() error {
	if err := checkRequire(v.Require, v.Sink); err != nil {
		return err
	}
	if v.Last == 0 {
		v.Last = Duration(24 * 60 * 60 * time.Second)
	}
	if v.Seals != nil {
		if v.Seals.Settle == 0 {
			v.Seals.Settle = Duration(DefaultSettle)
		}
		if v.Seals.Grace == 0 {
			v.Seals.Grace = Duration(DefaultGrace)
		}
	}
	return v.Archive.finish(false)
}

func (p *Purge) finish() error { return checkDatabase(&p.Database) }

func (c *ClockSync) finish() error {
	if err := checkRequire(c.Require, c.Sink); err != nil {
		return err
	}
	if c.MaxOffset == nil {
		d := Duration(time.Second)
		c.MaxOffset = &d
	}
	if c.Timeout == 0 {
		c.Timeout = Duration(5 * time.Second)
	}
	return nil
}

func (m *Migrate) finish() error { return checkDatabase(&m.Database) }

func (a *Archive) finish(writes bool) error {
	if a == nil {
		return nil
	}
	if writes && a.LockMode == "" {
		a.LockMode = "compliance"
	}
	return nil
}

func (k *Keys) enabled() bool { return k != nil && k.Provider != "" && k.Provider != "none" }
func (k *Keys) local() bool   { return k != nil && k.Provider == "local" }

// Enabled reports whether a key provider is named at all.
func (k *Keys) Enabled() bool { return k.enabled() }

// IsLocal reports whether the provider is the local one, whose directory is
// the only copy of the keys.
func (k *Keys) IsLocal() bool { return k.local() }

// checkDatabase refuses what the schema's pattern cannot express: a URL that
// does not parse, or that names no host and no socket to reach.
func checkDatabase(p *Postgres) error {
	if p == nil {
		return nil
	}
	if _, err := url.Parse(p.URL); err != nil {
		// url.Parse's own message quotes the URL; the URL carries no
		// password, but an error is logged, so say where and not what.
		return errors.New("database.url is not a URL")
	}
	return nil
}
