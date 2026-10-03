// Package sdk carries the files the consumer SDK publishes as data: the
// meta-schemas that govern catalogues, presets and extension slots, and the
// common catalogue of the component's own events.
//
// They are embedded so that a deployment gets them from the binary it already
// runs, rather than from files it has to ship and keep in step. Embedded files
// must sit inside the module that embeds them, which is why these live under
// sdk/ and not at the repository root.
package sdk

import "embed"

// Schemas are the meta-schemas under schemas/: catalogue, preset, extension.
//
//go:embed schemas/*.json
var Schemas embed.FS

// Catalogue is the common catalogue: the component's own meta-events, which
// every deployment carries.
//
//go:embed catalogue/*.yaml catalogue/*.json
var Catalogue embed.FS
