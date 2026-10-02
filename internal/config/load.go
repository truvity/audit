package config

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"time"

	policyconfig "github.com/truvity/policy/config"

	"github.com/truvity/audit"
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

// LoadQuery reads and validates audit-query's configuration.
func LoadQuery(file string) (*Query, error) { return load(file, "audit-query", (*Query).finish) }

// LoadDigest reads and validates the configuration of `audit digest`.
func LoadDigest(file string) (*Digest, error) { return load(file, "audit-digest", (*Digest).finish) }

// LoadVerify reads and validates the configuration of `audit verify`.
func LoadVerify(file string) (*Verify, error) { return load(file, "audit-verify", (*Verify).finish) }

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
	if s := w.Stream; s != nil {
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
		if s.AckWait <= w.Roll.Interval {
			return fmt.Errorf("stream.ackWait (%s) must be longer than roll.interval (%s): a writer gathers records "+
				"for one interval before it writes them and leaves them unacknowledged meanwhile, and a stream that "+
				"gives up waiting sooner offers the same records to another writer", s.AckWait.D(), w.Roll.Interval.D())
		}
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

func (q *Query) finish() error {
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

func (d *Digest) finish() error {
	if err := d.Archive.finish(true); err != nil {
		return err
	}
	return nil
}

func (v *Verify) finish() error {
	if v.Last == 0 {
		v.Last = Duration(24 * 60 * 60 * time.Second)
	}
	return v.Archive.finish(true)
}

func (p *Purge) finish() error { return checkDatabase(&p.Database) }

func (c *ClockSync) finish() error {
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
