package schemagen

import (
	"os"
	"path/filepath"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/record"
)

// OutDir is where the generated schema is written. It sits with the other
// generated code, not in the documents, so there is one copy of it.
const OutDir = "gen/jsonschema"

// FileName is the generated schema's file name, which carries the major
// version: a reader in a later year picks the one its records name.
const FileName = "record.v1.schema.json"

// ID is the identifier a record's schema is published under.
const ID = "https://schemas.truvity.com/audit/v1/record.schema.json"

// Record generates the JSON Schema of the canonical record.
func Record() ([]byte, error) {
	return Generate(
		(&auditv1.Record{}).ProtoReflect().Descriptor(),
		ID,
		"Audit record "+record.SchemaVersion,
		"One thing that happened, as seen by one source. This schema describes the "+
			"form this project writes: proto field names, enums as names, 64-bit integers "+
			"as strings, and unpopulated fields absent. Generated from proto/audit/v1/record.proto, "+
			"which is where a reader goes for what each field means.",
	)
}

// Write generates the schema and writes it into dir, returning the path.
func Write(dir string) (string, error) {
	b, err := Record()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
