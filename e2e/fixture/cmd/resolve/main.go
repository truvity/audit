// Command resolve prints e2e/fixture's names as shell variable
// assignments, so apply.sh and every hack script that installs onto the
// same names can `eval` this rather than repeating them.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/truvity/audit/e2e/fixture"
)

func main() {
	namespace := flag.String("namespace", "", "the namespace this install uses")
	release := flag.String("release", "", "the release name this install uses")
	flag.Parse()

	names, err := fixture.Resolve(fixture.Options{Namespace: *namespace, Release: *release})
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve:", err)
		os.Exit(1)
	}

	fields := map[string]string{
		"NAMESPACE":       names.Namespace,
		"RELEASE":         names.Release,
		"DATABASE_HOST":   names.DatabaseHost,
		"DATABASE":        names.DatabaseName,
		"OWNER_ROLE":      names.OwnerRole,
		"OWNER_SECRET":    names.OwnerSecret,
		"WRITER_ROLE":     names.WriterRole,
		"OBSERVE_ROLE":    names.ObserveRole,
		"OBSERVE_SECRET":  names.ObserveSecret,
		"QUERY_ROLE":      names.QueryRole,
		"WRITER_SECRET":   names.WriterSecret,
		"QUERY_SECRET":    names.QuerySecret,
		"STREAM_URL":      names.StreamURL,
		"STREAM":          names.StreamName,
		"STREAM_SUBJECT":  names.StreamSubject,
		"STREAM_CONSUMER": names.StreamConsumer,
		"BUCKET":          names.Bucket,
		"REGION":          names.Region,
		"ENDPOINT":        names.Endpoint,
		"S3_CREDS_SECRET": names.S3CredsSecret,
	}
	for name, value := range fields {
		fmt.Printf("%s=%q\n", name, value)
	}
}
