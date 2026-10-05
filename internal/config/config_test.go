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
	for _, name := range append(append([]string{}, schema.Names...), schema.Documents...) {
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
		{"observe", "audit-observe.full.yaml", &config.Observe{}, "audit-observe"},
		{"notary", "audit-notary.full.yaml", &config.Notary{}, "audit-notary"},
		{"writer as a Lambda", "audit-writer-lambda.full.yaml", &config.WriterLambda{}, "audit-writer-lambda"},
		{"verify with seals", "audit-verify.full.yaml", &config.Verify{}, "audit-verify"},
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
		"observe": func(p string) error { _, err := config.LoadObserve(p); return err },
		"migrate": func(p string) error { _, err := config.LoadMigrate(p); return err },
		"notary":  func(p string) error { _, err := config.LoadNotary(p); return err },
	} {
		body := map[string]string{
			"verify":  "deployment: /d.yaml\n" + archive,
			"purge":   "deployment: /d.yaml\ndatabase: {url: 'postgres://u@h/db'}\n",
			"clock":   "ntp: [time.example.test]\n",
			"migrate": "database: {url: 'postgres://u@h/db'}\nreader: audit_query\nwriter: audit_writer\nobserve: audit_observe\n",
			"observe": "archive: {bucket: {name: b}}\ndatabase: {url: 'postgres://u@h/db'}\n",
			"notary":  archive + "signer: {file: {path: /etc/audit/seal.pem}}\n",
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

func TestASinkIsAWriterOrAQueue(t *testing.T) {
	const q = "grants: /g.yaml\ndatabase: {url: 'postgres://u@h/db'}\n"
	const queue = "{sqs: {queueUrl: 'https://sqs.eu-west-1.amazonaws.com/1/audit.fifo', region: eu-west-1}"
	for name, c := range map[string]struct {
		sink string
		ok   bool
	}{
		"queue":                 {"sink: " + queue + "}\n", true},
		"queue meets queued":    {"sink: " + queue + "}\nrequire: queued\n", true},
		"queue cannot archive":  {"sink: " + queue + "}\nrequire: archived\n", false},
		"queue says archived":   {"sink: " + queue + ", expect: archived}\n", false},
		"queue with a token":    {"sink: " + queue + ", tokenFile: /t}\n", false},
		"url and queue":         {"sink: " + queue + ", url: 'http://a:8080'}\n", false},
		"neither":               {"sink: {}\n", false},
		"queue without a url":   {"sink: {sqs: {region: eu-west-1}}\n", false},
		"fifo not a fifo queue": {"sink: {sqs: {queueUrl: 'https://sqs.x/1/q', fifo: true}}\n", false},
	} {
		_, err := config.LoadQuery(write(t, q+c.sink))
		if c.ok != (err == nil) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A job takes the queue too, and the queue's acknowledgement is queued.
	if _, err := config.LoadClockSync(write(t, "ntp: [t.example.test]\nrequire: queued\nsink: "+queue+"}\n")); err != nil {
		t.Error(err)
	}
}

func TestObserveTakesItsDefaultsAndRefusesWhatItCannotUse(t *testing.T) {
	const base = "archive: {bucket: {name: b}}\ndatabase: {url: 'postgres://u@h/db'}\n"
	o, err := config.LoadObserve(write(t, base))
	if err != nil {
		t.Fatal(err)
	}
	if o.Settle.D().String() != "2m0s" || o.Interval.D().String() != "30s" || o.Batch != 500 || o.Listen.Address != ":8080" {
		t.Errorf("defaults not applied: %+v", o)
	}
	for name, body := range map[string]string{
		"no database":       "archive: {bucket: {name: b}}\n",
		"no archive":        "database: {url: 'postgres://u@h/db'}\n",
		"two wake sources":  base + "wake: {sqs: {queueUrl: 'https://sqs.example/q'}, nats: {subject: s, nats: {url: 'nats://n'}}}\n",
		"a lock mode":       "archive: {bucket: {name: b}, lockMode: compliance}\ndatabase: {url: 'postgres://u@h/db'}\n",
		"a password in url": "archive: {bucket: {name: b}}\ndatabase: {url: 'postgres://u:p@h/db'}\n",
		"a typo":            base + "setle: 1m\n",
	} {
		if _, err := config.LoadObserve(write(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The notary names exactly one place its key is, and gets the defaults a
// notary should not have to say: a ten-minute settle window and the record
// tier's lock.
func TestTheNotaryHasOneSignerAndItsDefaults(t *testing.T) {
	n, err := config.LoadNotary(write(t, "archive: {bucket: {name: b}}\nsigner: {kms: {key: alias/seal}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if n.Settle.D() != config.DefaultSettle || n.Archive.LockMode != "compliance" {
		t.Errorf("defaults not applied: %+v", n)
	}
	for name, body := range map[string]string{
		"two signers": "archive: {bucket: {name: b}}\nsigner: {kms: {key: k}, file: {path: /k.pem}}\n",
		"no signer":   "archive: {bucket: {name: b}}\nsigner: {}\n",
		"no archive":  "signer: {file: {path: /k.pem}}\n",
		"typo":        "archive: {bucket: {name: b}}\nsigner: {kms: {key: k}}\nsettel: 5m\n",
	} {
		if _, err := config.LoadNotary(write(t, body)); err == nil {
			t.Errorf("%s: the file was accepted", name)
		}
	}
}

// A verifier that checks seals pins at least one root: an empty list would
// trust nothing and say so only by failing every seal.
func TestSealVerificationPinsARoot(t *testing.T) {
	base := "deployment: /d.yaml\narchive: {bucket: {name: b}}\n"
	if _, err := config.LoadVerify(write(t, base+"seals: {roots: []}\n")); err == nil {
		t.Error("an empty list of roots was accepted")
	}
	v, err := config.LoadVerify(write(t, base+"seals: {roots: [AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Seals.Settle.D() != config.DefaultSettle || v.Seals.Grace.D() != config.DefaultGrace {
		t.Errorf("defaults not applied: %+v", v.Seals)
	}
}

func TestTheLambdaWriterTakesADynamoDBAndRefusesWhatItCannotRun(t *testing.T) {
	w, err := config.LoadWriterLambda(write(t, "deployment: /d.yaml\narchive: {bucket: {name: b}}\ndedupe: {dynamodb: {table: t}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if w.Require != "archived" || w.Archive.LockMode != "compliance" {
		t.Errorf("defaults not applied: %+v", w)
	}
	for name, body := range map[string]string{
		"no dedupe":        "deployment: /d.yaml\narchive: {bucket: {name: b}}\n",
		"an empty dedupe":  "deployment: /d.yaml\narchive: {bucket: {name: b}}\ndedupe: {}\n",
		"a database":       "deployment: /d.yaml\narchive: {bucket: {name: b}}\ndedupe: {dynamodb: {table: t}}\ndatabase: {url: 'postgres://u@h/db'}\n",
		"a listener":       "deployment: /d.yaml\narchive: {bucket: {name: b}}\ndedupe: {dynamodb: {table: t}}\nlisten: {address: ':8080'}\n",
		"no archive":       "deployment: /d.yaml\ndedupe: {dynamodb: {table: t}}\n",
		"a key in memory":  "deployment: /d.yaml\narchive: {bucket: {name: b}}\ndedupe: {dynamodb: {table: t}}\nkeys: {provider: local, local: {rootFile: /r}}\n",
		"a bad durability": "deployment: /d.yaml\narchive: {bucket: {name: b}}\ndedupe: {dynamodb: {table: t}}\nrequire: forever\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := config.LoadWriterLambda(write(t, body)); err == nil {
				t.Fatal("the file was accepted")
			}
		})
	}
}

// A file that does not say which version of the shape it is, is v1; one that
// says v1 is the same; one that says another is refused at the schema, before
// the typed decode could read it as something it is not.
func TestTheAPIVersionIsV1OrAbsent(t *testing.T) {
	for name, body := range map[string]string{
		"absent": minimalWriter, "v1": "apiVersion: audit.truvity.com/writer/v1\n" + minimalWriter,
	} {
		w, err := config.LoadWriter(write(t, body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if w.APIVersion != "" && w.APIVersion != "audit.truvity.com/writer/v1" {
			t.Errorf("%s: apiVersion = %q", name, w.APIVersion)
		}
	}
	_, err := config.LoadWriter(write(t, "apiVersion: audit.truvity.com/writer/v2\n"+minimalWriter))
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("a file of a version this build does not read was accepted or the refusal does not name the key: %v", err)
	}
	// Another kind of document is refused by name, not read as this one.
	_, err = config.LoadWriter(write(t, "apiVersion: audit.truvity.com/query/v1\n"+minimalWriter))
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("a query file was read as a writer's: %v", err)
	}
}

// The loader says which file it read and what was in it, in the form
// sha256sum prints, so that the writer's record of it can be checked by anyone
// holding the file.
func TestTheLoaderRecordsTheDigestOfWhatItRead(t *testing.T) {
	path := write(t, minimalWriter)
	w, err := config.LoadWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	if w.Source.File != path || w.Source.Digest != config.DigestBytes([]byte(minimalWriter)) {
		t.Errorf("source = %+v", w.Source)
	}
	if got, _ := config.DigestFile(path); got != w.Source.Digest {
		t.Errorf("DigestFile = %s, loader said %s", got, w.Source.Digest)
	}
	// sha256 of the bytes, as sha256sum prints it.
	if !strings.HasPrefix(w.Source.Digest, "sha256:") || len(w.Source.Digest) != len("sha256:")+64 {
		t.Errorf("digest = %q", w.Source.Digest)
	}
}

func TestADirectoryDigestMovesWithAFileAndWithItsName(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "a.yaml")
	first, err := config.DigestTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := config.DigestTree(dir); again != first {
		t.Fatalf("the same directory digests differently: %s, %s", first, again)
	}
	if err := os.Rename(filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")); err != nil {
		t.Fatal(err)
	}
	if renamed, _ := config.DigestTree(dir); renamed == first {
		t.Error("renaming a catalogue did not move the digest")
	}
	if err := os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("x: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if edited, _ := config.DigestTree(dir); edited == first {
		t.Error("editing a catalogue did not move the digest")
	}
	if none, err := config.DigestTree(""); none != "" || err != nil {
		t.Errorf("no directory is no digest, got %q, %v", none, err)
	}
}

// The documents a configuration names have schemas of their own, and each is
// the contract for what the code that reads it accepts.
func TestTheDocumentSchemasAcceptWhatTheCodeAcceptsAndRefuseWhatItWouldNot(t *testing.T) {
	for _, c := range []struct {
		name, doc string
		ok        bool
	}{
		{"audit-deployment", "profiles:\n  security: {presets: [iso27001]}\n", true},
		{"audit-deployment", "apiVersion: audit.truvity.com/deployment/v1\nprofiles:\n  security: {presets: [iso27001]}\n", true},
		{"audit-deployment", "apiVersion: audit.truvity.com/deployment/v2\nprofiles:\n  security: {presets: [iso27001]}\n", false},
		{"audit-deployment", "profiles: {}\n", false},
		{"audit-deployment", "profiles:\n  a/b: {presets: [iso27001]}\n", false},
		{"audit-deployment", "profiles:\n  security: {presets: [iso27001], retention: 1}\n", false},
		{"audit-grants", "rules:\n  - name: r\n    grant: {all_tenants: true, profiles: [security], operations: [search]}\n", true},
		{"audit-grants", "rules:\n  - name: r\n    grant: {all_tenants: true, profiles: [security], operations: [serach]}\n", false},
		{"audit-grants", "presets: [{name: other}]\n", false},
		{"audit-workloads", "issuers: [{url: 'https://i.example', audience: audit}]\n", true},
		{"audit-workloads", "issuers: []\n", false},
		{"audit-workloads", "issuers: [{url: 'https://i.example'}]\n", false},
	} {
		err := config.ValidateDocument(c.name, []byte(c.doc))
		if c.ok && err != nil {
			t.Errorf("%s refused %q: %v", c.name, c.doc, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s accepted %q", c.name, c.doc)
		}
	}
}
