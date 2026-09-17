package schemagen

import (
	"fmt"
	"path"

	"github.com/truvity/audit"
	"github.com/truvity/audit/record"
)

// OutDir is where the generated schema lives, with the other generated code.
const OutDir = "gen/jsonschema"

// FileName carries the major version: a reader in a later year picks the one
// its records name.
const FileName = "record.v1.schema.json"

// ID is the identifier a record's schema is published under.
const ID = "https://schemas.truvity.com/audit/v1/record.schema.json"

// Title and Description are what the published schema says about itself.
var (
	Title       = "Audit record " + record.SchemaVersion
	Description = "One thing that happened, as seen by one source. This schema describes the " +
		"form this project writes: proto field names, enums as names, 64-bit integers " +
		"as strings, and unpopulated fields absent. It is generated from " +
		"proto/audit/v1/record.proto together with that file's comments, and the proto " +
		"itself is archived beside it."
)

// Published returns the generated schema as it was published, which is what the
// archive keeps beside the records. It is read from the generated file rather
// than produced again, because the file is what a test holds to the proto and
// what a reader outside Go was given.
func Published() ([]byte, error) {
	body, err := audit.Generated.ReadFile(path.Join(OutDir, FileName))
	if err != nil {
		return nil, fmt.Errorf("schemagen: the published schema is not embedded: %w", err)
	}
	return body, nil
}
