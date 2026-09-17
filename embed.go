// Package audit carries the files this repository publishes as data: the
// framework presets, the meta-schemas that govern catalogues and presets, and
// the common catalogue of the component's own events.
//
// They are embedded so that a deployment gets them from the binary it already
// runs, rather than from files it has to ship and keep in step.
package audit

import "embed"

// Presets are the framework presets under presets/.
//
//go:embed presets/*.yaml
var Presets embed.FS

// Schemas are the meta-schemas under schemas/: catalogue, preset, extension.
//
//go:embed schemas/*.json
var Schemas embed.FS

// Proto is the schema of record itself. The writer copies the proto of a
// record's major beside the record's JSON Schema on first use, so the archive
// keeps the meaning of every field and not only its shape.
//
//go:embed proto/audit/v1/*.proto
var Proto embed.FS

// Generated holds what this build published for readers outside Go: the
// record's JSON Schema, with the proto's comments carried as descriptions. The
// writer puts a copy of it in the archive beside the records it describes.
//
//go:embed gen/jsonschema/*.json
var Generated embed.FS

// Catalogue is the common catalogue: the component's own meta-events, which
// every deployment carries.
//
//go:embed catalogue/*.yaml catalogue/*.json
var Catalogue embed.FS
