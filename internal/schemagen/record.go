package schemagen

import "github.com/truvity/audit/record"

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
