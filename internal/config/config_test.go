package config_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
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
	ctx := context.Background()
	env := config.NewSecrets(config.SecretsSource{})
	p := config.Postgres{URL: "postgres://audit@db.example.test:5432/audit", PasswordSecret: "AUDIT_TEST_DB_PASSWORD", MaxConnections: 7}
	cfg, err := p.PoolConfig(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Password != "s3cr:et@/x" || cfg.MaxConns != 7 || cfg.ConnConfig.User != "audit" {
		t.Errorf("the pool does not carry what the block names: %+v", cfg.ConnConfig.Config)
	}

	p.PasswordSecret = "AUDIT_TEST_NOT_SET"
	_, err = p.PoolConfig(ctx, env)
	// The refusal says which field and where it looked, and does not quote what the
	// field holds.
	if err == nil || !strings.Contains(err.Error(), "database.passwordSecret") || !strings.Contains(err.Error(), "is not set") ||
		strings.Contains(err.Error(), "AUDIT_TEST_NOT_SET") {
		t.Errorf("an unset variable must be refused by field, not by quoting its name: %v", err)
	}
	// And a variable nobody named is never read: PGPASSWORD is not a way in.
	t.Setenv("AUDIT_TEST_UNNAMED", "x")
	p.PasswordSecret = ""
	cfg, err = p.PoolConfig(ctx, env)
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
// says v1 under the old group is the same; v2 is read as it is; one that says
// another is refused at the schema, before the typed decode could read it as
// something it is not.
func TestTheAPIVersionIsV2OrTheDeprecatedV1(t *testing.T) {
	const v2 = "audit.truvity.github.io/audit-writer/v2"
	for name, body := range map[string]string{
		"absent": minimalWriter,
		"v1":     "apiVersion: truvity.github.io/audit-writer/v1\n" + minimalWriter,
		"v2":     "apiVersion: " + v2 + "\n" + minimalWriter,
	} {
		w, err := config.LoadWriter(write(t, body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// Whatever it was written in, the loader hands back version 2.
		if w.APIVersion != v2 && name != "v2" {
			t.Errorf("%s: apiVersion = %q, want it converted to %s", name, w.APIVersion, v2)
		}
	}
	for name, v := range map[string]string{
		"v3":                  "audit.truvity.github.io/audit-writer/v3",
		"the old group at v2": "truvity.github.io/audit-writer/v2",
		"the new group at v1": "audit.truvity.github.io/audit-writer/v1",
		"another kind, v2":    "audit.truvity.github.io/audit-query/v2",
		"another kind, v1":    "truvity.github.io/audit-query/v1",
		"not of the form":     "v2",
		"another group":       "example.com/audit-writer/v2",
	} {
		_, err := config.LoadWriter(write(t, "apiVersion: "+v+"\n"+minimalWriter))
		if err == nil || !strings.Contains(err.Error(), "apiVersion") {
			t.Errorf("%s: accepted, or the refusal does not name the key: %v", name, err)
		}
	}
}

// A version-1 file is converted, not reinterpreted: the fields that named an
// environment variable name a secret, and the file's secrets are the environment.
func TestAVersion1FileIsConvertedAndItsEnvFieldsBecomeSecrets(t *testing.T) {
	t.Setenv("AUDIT_TEST_DB_PASSWORD", "from-the-environment")
	body := minimalWriter + "database: {url: 'postgres://u@h/db', passwordEnv: AUDIT_TEST_DB_PASSWORD}\n" +
		"keys: {provider: transit, transit: {openbao: {address: 'https://b.example.test', tokenEnv: AUDIT_TEST_DB_PASSWORD}}}\n"
	w, err := config.LoadWriter(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if w.Database.PasswordSecret != "AUDIT_TEST_DB_PASSWORD" || w.Keys.Transit.OpenBAO.TokenSecret != "AUDIT_TEST_DB_PASSWORD" {
		t.Errorf("the Env fields were not carried to Secret: %+v %+v", w.Database, w.Keys.Transit.OpenBAO)
	}
	if got := w.SecretReader().Source(); got != config.SourceEnv {
		t.Errorf("a converted file reads its secrets from %q, want env", got)
	}
	pool, err := w.Database.PoolConfig(context.Background(), w.SecretReader())
	if err != nil || pool.ConnConfig.Password != "from-the-environment" {
		t.Errorf("the converted password does not resolve: %v", err)
	}
	// A v2 file does not take the old spelling: it is not v1's reading.
	_, err = config.LoadWriter(write(t, "apiVersion: audit.truvity.github.io/audit-writer/v2\n"+minimalWriter+
		"database: {url: 'postgres://u@h/db', passwordEnv: X}\n"))
	if err == nil || !strings.Contains(err.Error(), "passwordEnv") {
		t.Errorf("passwordEnv in a version-2 file was accepted or not named: %v", err)
	}
}

// The v1 schemas are frozen and the examples written in them are what v1 files
// look like; a v2 example does not validate against them, and the reverse.
func TestTheFrozenVersion1SchemasStillAcceptTheVersion1Examples(t *testing.T) {
	for _, c := range []struct{ file, name string }{
		{"audit-writer.full.yaml", "audit-writer"}, {"audit-writer.consume.full.yaml", "audit-writer"},
		{"audit-writer.receiver.full.yaml", "audit-writer"}, {"audit-query.full.yaml", "audit-query"},
		{"audit-observe.full.yaml", "audit-observe"}, {"audit-notary.full.yaml", "audit-notary"},
		{"audit-writer-lambda.full.yaml", "audit-writer-lambda"}, {"audit-verify.full.yaml", "audit-verify"},
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", "v1", c.file))
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if err := config.ValidateLegacy(c.name, doc); err != nil {
			t.Errorf("%s: the v1 example does not validate against the v1 schema: %v", c.file, err)
		}
		if err := config.Validate(c.name, doc); err == nil {
			t.Errorf("%s: a v1 example validates against the v2 schema", c.file)
		}
		// And it loads, converted.
		p := filepath.Join("testdata", "v1", c.file)
		var err2 error
		switch c.name {
		case "audit-writer":
			_, err2 = config.LoadWriter(p)
		case "audit-query":
			_, err2 = config.LoadQuery(p)
		case "audit-observe":
			_, err2 = config.LoadObserve(p)
		case "audit-notary":
			_, err2 = config.LoadNotary(p)
		case "audit-writer-lambda":
			_, err2 = config.LoadWriterLambda(p)
		case "audit-verify":
			_, err2 = config.LoadVerify(p)
		}
		if err2 != nil {
			t.Errorf("%s: a v1 file is not read: %v", c.file, err2)
		}
	}
}

// The secrets block: one source for the file, and a name cannot leave its root.
func TestSecretsAreReadFromTheDeclaredSourceOnly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db", "password"), []byte("from-a-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("db", "from-the-environment")

	files := config.NewSecrets(config.SecretsSource{Source: config.SourceFile, Root: dir})
	if v, err := files.Get(ctx, "database.passwordSecret", "db/password"); err != nil || v != "from-a-file" {
		t.Errorf("file: %q, %v", v, err)
	}
	for _, name := range []string{"../etc/passwd", "/etc/passwd", "db/../../x", "", "db//password", ".hidden/../x"} {
		if _, err := files.Get(ctx, "f", name); err == nil {
			t.Errorf("the name %q was accepted by the file source", name)
		}
	}
	if _, err := files.Get(ctx, "f", "empty"); err == nil {
		t.Error("an empty secret file was accepted")
	}
	_, err := files.Get(ctx, "database.passwordSecret", "absent")
	if err == nil || !strings.Contains(err.Error(), "database.passwordSecret") || !strings.Contains(err.Error(), dir) ||
		strings.Contains(err.Error(), "absent") {
		t.Errorf("a missing secret must be refused by field and root, without quoting its name: %v", err)
	}
	// The file source never looks at the environment, nor the reverse.
	if _, err := files.Get(ctx, "f", "db"); err == nil {
		t.Error("the file source read an environment variable")
	}
	env := config.NewSecrets(config.SecretsSource{})
	if v, err := env.Get(ctx, "f", "db"); err != nil || v != "from-the-environment" {
		t.Errorf("env: %q, %v", v, err)
	}
	if _, err := env.Get(ctx, "f", "db/password"); err == nil {
		t.Error("the env source took a path")
	}
}

type fakeSSM struct {
	params map[string]string
	asked  []string
}

func (f *fakeSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.asked = append(f.asked, aws.ToString(in.Name))
	if !aws.ToBool(in.WithDecryption) {
		return nil, errors.New("asked without decryption")
	}
	v, ok := f.params[aws.ToString(in.Name)]
	if !ok {
		return nil, errors.New("ParameterNotFound")
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(v)}}, nil
}

func TestTheSSMSourceReadsOneParameterUnderItsRootDecrypted(t *testing.T) {
	fake := &fakeSSM{params: map[string]string{"/audit/main/private/config/openbao/token": "s.token"}}
	old := config.OpenSSM
	config.OpenSSM = func(context.Context) (config.ParameterAPI, error) { return fake, nil }
	t.Cleanup(func() { config.OpenSSM = old })

	s := config.NewSecrets(config.SecretsSource{Source: config.SourceSSM, Root: "/audit/main/private/config"})
	v, err := s.Get(context.Background(), "openbao.tokenSecret", "openbao/token")
	if err != nil || v != "s.token" {
		t.Fatalf("%q, %v", v, err)
	}
	if len(fake.asked) != 1 || fake.asked[0] != "/audit/main/private/config/openbao/token" {
		t.Errorf("asked %v", fake.asked)
	}
	// A name cannot climb out of the root, so a file cannot point the function at
	// another installation's parameters.
	if _, err := s.Get(context.Background(), "f", "../../other/token"); err == nil {
		t.Error("a name climbing out of the root was accepted")
	}
	if len(fake.asked) != 1 {
		t.Errorf("SSM was asked for a name that was refused: %v", fake.asked)
	}
	_, err = s.Get(context.Background(), "openbao.tokenSecret", "missing")
	if err == nil || !strings.Contains(err.Error(), "openbao.tokenSecret") || !strings.Contains(err.Error(), "/audit/main/private/config") ||
		strings.Contains(err.Error(), "missing") {
		t.Errorf("a missing parameter must be refused by field and root, without quoting its name: %v", err)
	}
}

func TestTheSecretsBlockIsHeldToItsSource(t *testing.T) {
	const lambda = "deployment: /d\narchive: {bucket: {name: b}}\ndedupe: {dynamodb: {table: t}}\n"
	for name, c := range map[string]struct {
		block string
		ok    bool
	}{
		"ssm with a root":           {"secrets: {source: ssm, root: /audit/main/private/config}\n", true},
		"ssm without a root":        {"secrets: {source: ssm}\n", false},
		"ssm with a relative root":  {"secrets: {source: ssm, root: audit/main}\n", false},
		"ssm with a trailing /":     {"secrets: {source: ssm, root: /audit/main/}\n", false},
		"file with a root":          {"secrets: {source: file, root: /etc/audit/secrets}\n", true},
		"file with a relative root": {"secrets: {source: file, root: secrets}\n", false},
		"file without a root":       {"secrets: {source: file}\n", false},
		"env":                       {"secrets: {source: env}\n", true},
		"env with a root":           {"secrets: {source: env, root: /x}\n", false},
		"another source":            {"secrets: {source: vault, root: /x}\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.LoadWriterLambda(write(t, "apiVersion: audit.truvity.github.io/audit-writer-lambda/v2\n"+c.block+lambda+
				"keys: {provider: transit, transit: {openbao: {address: 'https://b.example.test', tokenSecret: openbao/token}}}\n"))
			if (err == nil) != c.ok {
				t.Errorf("ok = %v, got %v", c.ok, err)
			}
		})
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
		{"audit-deployment", "apiVersion: truvity.github.io/audit-deployment/v1\nprofiles:\n  security: {presets: [iso27001]}\n", true},
		{"audit-deployment", "apiVersion: audit.truvity.github.io/audit-deployment/v2\nprofiles:\n  security: {presets: [iso27001]}\n", true},
		{"audit-deployment", "apiVersion: truvity.github.io/audit-deployment/v2\nprofiles:\n  security: {presets: [iso27001]}\n", false},
		{"audit-deployment", "apiVersion: audit.truvity.github.io/audit-deployment/v1\nprofiles:\n  security: {presets: [iso27001]}\n", false},
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

// On Lambda the environment is not a place for a secret, whatever the file says,
// and a version-1 ...Env that was converted to source env fails there too.
func TestTheEnvSourceIsRefusedOnLambda(t *testing.T) {
	t.Setenv("AUDIT_TEST_DB_PASSWORD", "x")
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "audit-writer")
	_, err := config.NewSecrets(config.SecretsSource{}).Get(context.Background(), "database.passwordSecret", "AUDIT_TEST_DB_PASSWORD")
	if err == nil || !strings.Contains(err.Error(), "database.passwordSecret") || !strings.Contains(err.Error(), "AWS Lambda") {
		t.Errorf("env on Lambda: %v", err)
	}
	if strings.Contains(err.Error(), "AUDIT_TEST_DB_PASSWORD") {
		t.Errorf("the refusal quotes the name: %v", err)
	}
	w, err := config.LoadWriter(write(t, minimalWriter+"database: {url: 'postgres://u@h/db', passwordEnv: AUDIT_TEST_DB_PASSWORD}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Database.PoolConfig(context.Background(), w.SecretReader()); err == nil {
		t.Error("a converted v1 passwordEnv was read on Lambda")
	}
}

// A client that could not be made is tried again on the next read, after a wait,
// and not kept as the answer for the life of the process.
func TestAFailedSSMClientIsRetried(t *testing.T) {
	fake := &fakeSSM{params: map[string]string{"/audit/main/private/config/a": "v"}}
	calls := 0
	old := config.OpenSSM
	config.OpenSSM = func(context.Context) (config.ParameterAPI, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("no credentials yet")
		}
		return fake, nil
	}
	t.Cleanup(func() { config.OpenSSM = old })
	s := config.NewSecrets(config.SecretsSource{Source: config.SourceSSM, Root: "/audit/main/private/config"})
	if _, err := s.Get(context.Background(), "f", "a"); err == nil {
		t.Fatal("the first read succeeded without a client")
	}
	// Inside the wait the failure is the answer and the client is not made again.
	if _, err := s.Get(context.Background(), "f", "a"); err == nil || calls != 1 {
		t.Fatalf("inside the backoff: %v after %d calls", err, calls)
	}
	time.Sleep(1100 * time.Millisecond)
	if v, err := s.Get(context.Background(), "f", "a"); err != nil || v != "v" {
		t.Fatalf("after the backoff: %q, %v", v, err)
	}
}
