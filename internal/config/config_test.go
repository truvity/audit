package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	policyconfig "github.com/truvity/policy/config"
	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/audit/internal/config"
	"github.com/truvity/audit/internal/config/schema"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A schema in schemas/config is what schema.Schema writes. The binaries read
// the committed file, and a chart's tests validate against it, so a change to
// the builder that is not followed by `just config-schemas` fails here as well
// as in the drift check.
func TestTheCommittedSchemasAreTheGeneratedOnes(t *testing.T) {
	for _, name := range schema.Names {
		want, ok := schema.Schema(name)
		if !ok {
			t.Fatalf("%s: no schema is built", name)
		}
		got, err := os.ReadFile(filepath.Join("..", "..", "schemas", "config", name+".schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is not what `just config-schemas` writes", name)
		}
	}
}

// Each type is held to its schema in both directions: a file that sets every
// key must decode into the type with no key left over, and writing the type
// back must give the file again. A key added to the schema and not the type is
// refused by the first; one added to the type and not the schema, by the
// second.
func TestTheTypesAndTheSchemasDescribeTheSameKeys(t *testing.T) {
	for _, c := range []struct {
		name, file string
		into       any
		validate   string
	}{
		{"writer", "audit-writer.full.yaml", &config.Writer{}, "audit-writer"},
		{"writer consuming SQS", "audit-writer.consume.full.yaml", &config.Writer{}, "audit-writer"},
		{"receiver forwarding to SQS", "audit-writer.receiver.full.yaml", &config.Writer{}, "audit-writer"},
		{"query", "audit-query.full.yaml", &config.Query{}, "audit-query"},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", c.file))
			if err != nil {
				t.Fatal(err)
			}
			var doc any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if err := config.Validate(c.validate, doc); err != nil {
				t.Fatalf("the full example does not validate: %v", err)
			}
			asJSON, _ := json.Marshal(doc)
			dec := json.NewDecoder(bytes.NewReader(asJSON))
			dec.DisallowUnknownFields()
			if err := dec.Decode(c.into); err != nil {
				t.Fatalf("the type does not hold a key the example sets: %v", err)
			}
			back, err := json.Marshal(c.into)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			_ = json.Unmarshal(asJSON, &want)
			_ = json.Unmarshal(back, &got)
			if !jsonEqual(want, got) {
				t.Errorf("the type does not write back what the example sets:\n want %s\n  got %s", asJSON, back)
			}
		})
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

const minimalWriter = `
deployment: /etc/audit/deployment.yaml
anonymousWrites: true
archive:
  bucket:
    name: audit-archive
`

func TestAValidFileLoadsWithItsDefaults(t *testing.T) {
	w, err := config.LoadWriter(write(t, minimalWriter))
	if err != nil {
		t.Fatal(err)
	}
	if w.Mode != "writer" || w.Listen.Address != ":8080" || w.Replicas != 1 ||
		w.Archive.LockMode != "compliance" || w.Roll.MaxRecords != 5000 || w.Roll.Interval.D().String() != "30s" {
		t.Errorf("defaults not applied: %+v", w)
	}
}

func TestEveryJobTakesAValidFile(t *testing.T) {
	archive := "archive: {bucket: {name: b}}\n"
	for name, load := range map[string]func(string) error{
		"verify":  func(p string) error { _, err := config.LoadVerify(p); return err },
		"purge":   func(p string) error { _, err := config.LoadPurge(p); return err },
		"clock":   func(p string) error { _, err := config.LoadClockSync(p); return err },
		"migrate": func(p string) error { _, err := config.LoadMigrate(p); return err },
	} {
		body := map[string]string{
			"verify":  "deployment: /d.yaml\n" + archive,
			"purge":   "deployment: /d.yaml\ndatabase: {url: 'postgres://u@h/db'}\n",
			"clock":   "ntp: [time.example.test]\n",
			"migrate": "database: {url: 'postgres://u@h/db'}\nreader: audit_query\n",
		}[name]
		if err := load(write(t, body)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// What a typo must do: fail, and say which key.
func TestAnUnknownKeyIsRefusedAndNamed(t *testing.T) {
	_, err := config.LoadWriter(write(t, minimalWriter+"archive2: {}\nlisten: {adr: ':1'}\n"))
	var ce *policyconfig.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want a configuration error, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"'archive2'", "listen: additional properties 'adr'"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not name %q: %s", want, msg)
		}
	}
}

func TestAMissingRequiredKeyIsRefusedAndNamed(t *testing.T) {
	_, err := config.LoadWriter(write(t, "anonymousWrites: true\narchive: {bucket: {name: b}}\n"))
	if err == nil || !strings.Contains(err.Error(), "deployment") {
		t.Fatalf("want a refusal naming deployment, got %v", err)
	}
	// A key required only in one mode.
	_, err = config.LoadWriter(write(t, "deployment: /d\nanonymousWrites: true\n"))
	if err == nil || !strings.Contains(err.Error(), "archive") {
		t.Fatalf("a writer with no archive: want a refusal naming archive, got %v", err)
	}
}

// A secret is never in the file: not under a key of its own, which no schema
// has, and not inside a URL, which would have been the easy place.
func TestASecretInTheFileIsRefused(t *testing.T) {
	_, err := config.LoadWriter(write(t, minimalWriter+"database: {url: 'postgres://u:hunter2@h/db'}\n"))
	if err == nil {
		t.Fatal("a password inside the database URL was accepted")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the error quotes the secret: %v", err)
	}
	_, err = config.LoadWriter(write(t, minimalWriter+"database: {url: 'postgres://u@h/db', password: hunter2}\n"))
	if err == nil || !strings.Contains(err.Error(), "database: additional properties 'password'") {
		t.Errorf("a password key was not refused by name: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the error quotes the secret: %v", err)
	}
	_, err = config.LoadWriter(write(t, minimalWriter+"keys: {provider: transit, transit: {openbao: {address: 'https://b.example.test', token: s.abc}}}\n"))
	if err == nil {
		t.Error("an OpenBAO token in the file was accepted")
	}
}

// The environment supplies exactly the secrets the file names.
func TestADeclaredSecretIsReadFromTheEnvironment(t *testing.T) {
	t.Setenv("AUDIT_TEST_DB_PASSWORD", "s3cr:et@/x")
	p := config.Postgres{URL: "postgres://audit@db.example.test:5432/audit", PasswordEnv: "AUDIT_TEST_DB_PASSWORD", MaxConnections: 7}
	cfg, err := p.PoolConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Password != "s3cr:et@/x" || cfg.MaxConns != 7 || cfg.ConnConfig.User != "audit" {
		t.Errorf("the pool does not carry what the block names: %+v", cfg.ConnConfig.Config)
	}

	p.PasswordEnv = "AUDIT_TEST_NOT_SET"
	_, err = p.PoolConfig()
	if err == nil || !strings.Contains(err.Error(), "AUDIT_TEST_NOT_SET") {
		t.Errorf("an unset variable must be named: %v", err)
	}
	// And a variable nobody named is never read: PGPASSWORD is not a way in.
	t.Setenv("AUDIT_TEST_UNNAMED", "x")
	p.PasswordEnv = ""
	cfg, err = p.PoolConfig()
	if err != nil || cfg.ConnConfig.Password == "x" {
		t.Errorf("a variable the file did not name reached the connection: %v", err)
	}
}

func TestAReceiverHoldsNeitherTheArchiveNorKeys(t *testing.T) {
	const receiver = `
mode: receiver
deployment: /d.yaml
anonymousWrites: true
stream:
  nats: {url: 'nats://n:4222'}
`
	if _, err := config.LoadWriter(write(t, receiver)); err != nil {
		t.Fatalf("a receiver: %v", err)
	}
	for name, extra := range map[string]string{
		"archive": "archive: {bucket: {name: b}}\n",
		"keys":    "keys: {provider: none}\n",
	} {
		if _, err := config.LoadWriter(write(t, receiver+extra)); err == nil {
			t.Errorf("a receiver with %s was accepted", name)
		}
	}
	if _, err := config.LoadWriter(write(t, "mode: receiver\ndeployment: /d\nanonymousWrites: true\n")); err == nil {
		t.Error("a receiver with no stream was accepted: there is nowhere to publish to")
	}
}

func TestTheStreamMustOutwaitTheRoll(t *testing.T) {
	_, err := config.LoadWriter(write(t, minimalWriter+"stream: {nats: {url: 'nats://n:4222'}, ackWait: 20s}\nroll: {interval: 30s}\n"))
	if err == nil || !strings.Contains(err.Error(), "ackWait") {
		t.Fatalf("want a refusal naming ackWait, got %v", err)
	}
}

func TestExactlyOneWayToVerifyCallers(t *testing.T) {
	both := minimalWriter + "workloads: /w.yaml\n"
	if _, err := config.LoadWriter(write(t, both)); err == nil {
		t.Error("anonymous writes and a workloads file were both accepted")
	}
	neither := strings.Replace(minimalWriter, "anonymousWrites: true\n", "", 1)
	if _, err := config.LoadWriter(write(t, neither)); err == nil {
		t.Error("a writer that neither verifies callers nor says it accepts anybody was accepted")
	}
}

func TestOneWayToSignInToOpenBAO(t *testing.T) {
	const base = "deployment: /d\nanonymousWrites: true\narchive: {bucket: {name: b}}\n"
	open := func(auth string) string {
		return base + "keys: {provider: transit, transit: {openbao: {address: 'https://b.example.test'" + auth + "}}}\n"
	}
	if _, err := config.LoadWriter(write(t, open(", tokenEnv: BAO_TOKEN"))); err != nil {
		t.Errorf("a token from a named variable: %v", err)
	}
	if _, err := config.LoadWriter(write(t, open(""))); err == nil {
		t.Error("no way to sign in was accepted")
	}
	if _, err := config.LoadWriter(write(t, open(", tokenEnv: A, tokenFile: /t"))); err == nil {
		t.Error("two ways to sign in were accepted")
	}
}

func TestResolveNeedsTheArchive(t *testing.T) {
	const q = `
grants: /g.yaml
sink: {url: 'http://audit:8080'}
database: {url: 'postgres://u@h/db'}
keys: {provider: local, local: {rootFile: /r, dir: /d}}
`
	if _, err := config.LoadQuery(write(t, q)); err == nil || !strings.Contains(err.Error(), "archive") {
		t.Fatalf("resolve with no archive: %v", err)
	}
	if _, err := config.LoadQuery(write(t, q+"archive: {bucket: {name: b}}\n")); err != nil {
		t.Fatal(err)
	}
}

func TestTheExportsAreNotTheArchive(t *testing.T) {
	const q = `
grants: /g.yaml
sink: {url: 'http://audit:8080'}
database: {url: 'postgres://u@h/db'}
archive: {bucket: {name: same}}
exports: {bucket: {name: same}}
`
	if _, err := config.LoadQuery(write(t, q)); err == nil || !strings.Contains(err.Error(), "exports.bucket") {
		t.Fatalf("exports into the archive's bucket: %v", err)
	}
}

func TestTheS3scanSearcherNeedsNoDatabaseButTheArchive(t *testing.T) {
	const q = "grants: /g.yaml\nsink: {url: 'http://audit:8080'}\nsearcher: s3scan\n"
	if _, err := config.LoadQuery(write(t, q)); err == nil {
		t.Error("s3scan with no archive was accepted")
	}
	if _, err := config.LoadQuery(write(t, q+"archive: {bucket: {name: b}}\n")); err != nil {
		t.Error(err)
	}
}

func TestAnEmptyOrMissingFileIsRefused(t *testing.T) {
	if _, err := config.LoadWriter(write(t, "")); err == nil {
		t.Error("an empty file was accepted")
	}
	if _, err := config.LoadWriter(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("a missing file was accepted")
	}
}

const receiverHead = "mode: receiver\ndeployment: /d.yaml\nanonymousWrites: true\n"

func TestTheDefaultRequireIsTheStrongestTheModeCanGive(t *testing.T) {
	w, err := config.LoadWriter(write(t, minimalWriter))
	if err != nil || w.Require != "archived" {
		t.Errorf("a writer: %v require=%v", err, w)
	}
	r, err := config.LoadWriter(write(t, receiverHead+"forward: {nats: {nats: {url: 'nats://n:4222'}}}\n"))
	if err != nil || r.Require != "queued" {
		t.Errorf("a receiver: %v require=%v", err, r)
	}
}

func TestEachTransportLoadsAndTheOthersAreRefused(t *testing.T) {
	const sqs = "{queueUrl: 'https://sqs.example.test/ACCOUNT/audit'}"
	for name, c := range map[string]struct {
		body string
		ok   bool
	}{
		"forward nats":          {receiverHead + "forward: {nats: {nats: {url: 'nats://n:4222'}}}\n", true},
		"forward sqs":           {receiverHead + "forward: {sqs: " + sqs + "}\n", true},
		"forward log":           {receiverHead + "require: logged\nforward: {log: {}}\n", true},
		"forward two":           {receiverHead + "forward: {sqs: " + sqs + ", log: {}}\n", false},
		"forward none":          {receiverHead + "forward: {}\n", false},
		"log needs require":     {receiverHead + "forward: {log: {}}\n", false},
		"log with queued":       {receiverHead + "require: queued\nforward: {log: {}}\n", false},
		"receiver archived":     {receiverHead + "require: archived\nforward: {sqs: " + sqs + "}\n", false},
		"receiver no forward":   {receiverHead, false},
		"stream and forward":    {receiverHead + "stream: {nats: {url: 'nats://n:4222'}}\nforward: {sqs: " + sqs + "}\n", false},
		"receiver consumes":     {receiverHead + "forward: {sqs: " + sqs + "}\nconsume: {sqs: " + sqs + "}\n", false},
		"writer consume sqs":    {minimalWriter + "consume: {sqs: " + sqs + "}\n", true},
		"writer consume two":    {minimalWriter + "consume: {sqs: " + sqs + ", nats: {nats: {url: 'nats://n:4222'}}}\n", false},
		"writer forwards":       {minimalWriter + "forward: {sqs: " + sqs + "}\n", false},
		"writer both":           {minimalWriter + "stream: {nats: {url: 'nats://n:4222'}}\nconsume: {sqs: " + sqs + "}\n", false},
		"writer require logged": {minimalWriter + "require: logged\n", true},
		"fifo disagrees":        {minimalWriter + "consume: {sqs: {queueUrl: 'https://sqs.example.test/ACCOUNT/audit', fifo: true}}\n", false},
		"require unknown":       {minimalWriter + "require: durable\n", false},
		"sqs secret key":        {minimalWriter + "consume: {sqs: {queueUrl: 'https://sqs.example.test/ACCOUNT/audit', accessKey: x}}\n", false},
	} {
		_, err := config.LoadWriter(write(t, c.body))
		if c.ok && err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: was accepted", name)
		}
	}
}

func TestTheRefusalsSaySWhy(t *testing.T) {
	_, err := config.LoadWriter(write(t, receiverHead+"require: archived\nforward: {log: {}}\n"))
	if err == nil {
		t.Fatal("accepted")
	}
	_, err = config.LoadWriter(write(t, receiverHead+"require: queued\nforward: {log: {}}\n"))
	if err == nil || !strings.Contains(err.Error(), "logged") {
		t.Errorf("log with queued should name logged: %v", err)
	}
}

func TestAnEmitterRequireNeedsWhatTheWriterIsSaidToGive(t *testing.T) {
	const q = "grants: /g.yaml\ndatabase: {url: 'postgres://u@h/db'}\n"
	for name, c := range map[string]struct {
		sink string
		ok   bool
	}{
		"expect meets":   {"sink: {url: 'http://a:8080', expect: archived}\nrequire: queued\n", true},
		"expect equals":  {"sink: {url: 'http://a:8080', expect: queued}\nrequire: queued\n", true},
		"expect is weak": {"sink: {url: 'http://a:8080', expect: logged}\nrequire: queued\n", false},
		"no expect":      {"sink: {url: 'http://a:8080'}\nrequire: queued\n", false},
		"no require":     {"sink: {url: 'http://a:8080'}\n", true},
		"expect only":    {"sink: {url: 'http://a:8080', expect: queued}\n", true},
	} {
		_, err := config.LoadQuery(write(t, q+c.sink))
		if c.ok != (err == nil) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A job without a sink has nothing to hold to a requirement.
	if _, err := config.LoadClockSync(write(t, "ntp: [t.example.test]\nrequire: queued\n")); err == nil {
		t.Error("require with no sink was accepted")
	}
	if _, err := config.LoadClockSync(write(t, "ntp: [t.example.test]\nrequire: queued\nsink: {url: 'http://a', expect: archived}\n")); err != nil {
		t.Error(err)
	}
}
