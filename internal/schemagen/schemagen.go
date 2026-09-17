// Package schemagen turns the record's protobuf descriptor into a JSON Schema
// of the form this project writes.
//
// The schema is generated rather than written by hand so that it cannot drift
// from the proto, and it describes the *written* form: proto field names,
// enums as names, 64-bit integers as strings, unpopulated fields absent. A
// reader outside Go validates an archived record against it, which is the
// whole point of publishing it: the archive is self-describing for as long as
// it is kept, without this repository.
//
// It carries the proto's comments as descriptions, which is why it is produced
// by a buf plugin at generation time and not by the binary at run time: the
// descriptor compiled into a Go binary has no source information in it, and a
// schema without its meaning would leave an archive structurally described and
// semantically mute.
package schemagen

import (
	"encoding/json"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Comments are the leading comments of descriptors, by full name, flattened to
// one paragraph each.
type Comments map[protoreflect.FullName]string

// Generate returns the JSON Schema of a message and everything it contains,
// carrying the comments given as descriptions. The description argument is a
// note about the schema itself and lands in $comment; what the record means is
// the proto's to say.
func Generate(md protoreflect.MessageDescriptor, id, title, description string, comments Comments) ([]byte, error) {
	g := &generator{defs: map[string]map[string]any{}, seen: map[string]bool{}, comments: comments}
	root := g.message(md)
	schema := map[string]any{
		"$schema":  "https://json-schema.org/draft/2020-12/schema",
		"$id":      id,
		"title":    title,
		"$comment": description,
	}
	for k, v := range root {
		schema[k] = v
	}
	if len(g.defs) > 0 {
		schema["$defs"] = g.defs
	}
	out, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

type generator struct {
	defs     map[string]map[string]any
	seen     map[string]bool
	comments Comments
}

// message describes one message inline. Nested message types go to $defs and
// are referenced, which is what keeps a recursive type finite.
func (g *generator) message(md protoreflect.MessageDescriptor) map[string]any {
	properties := map[string]any{}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		p := g.field(fd)
		// The field's own comment says more than its type's, and wins.
		if c := g.comments[fd.FullName()]; c != "" {
			p["description"] = c
		}
		properties[string(fd.TextName())] = p
	}
	out := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if c := g.comments[md.FullName()]; c != "" {
		out["description"] = c
	}
	return out
}

func (g *generator) field(fd protoreflect.FieldDescriptor) map[string]any {
	switch {
	case fd.IsMap():
		value := g.value(fd.MapValue())
		if fd.MapKey().Kind() != protoreflect.StringKind {
			// protobuf JSON writes every map key as a string.
			value["description"] = "keys are the string form of " + fd.MapKey().Kind().String()
		}
		return map[string]any{"type": "object", "additionalProperties": value}
	case fd.IsList():
		return map[string]any{"type": "array", "items": g.value(fd)}
	default:
		return g.value(fd)
	}
}

// value describes one value, ignoring repetition.
func (g *generator) value(fd protoreflect.FieldDescriptor) map[string]any {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.StringKind:
		return map[string]any{"type": "string"}
	case protoreflect.BytesKind:
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	case protoreflect.EnumKind:
		return g.enum(fd.Enum())
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return map[string]any{"type": "integer"}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		// 64-bit integers are written as strings, because a JSON number cannot
		// hold them all exactly.
		return map[string]any{"type": "string", "pattern": `^-?[0-9]+$`}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return map[string]any{"type": "number"}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return g.reference(fd.Message())
	default:
		return map[string]any{}
	}
}

// reference is a message-valued field: a well-known type written in its own
// way, or a reference into $defs.
func (g *generator) reference(md protoreflect.MessageDescriptor) map[string]any {
	switch md.FullName() {
	case "google.protobuf.Timestamp":
		return map[string]any{
			"type": "string", "format": "date-time",
			"description": "RFC 3339 in UTC.",
		}
	case "google.protobuf.Duration":
		return map[string]any{"type": "string", "pattern": `^-?[0-9]+(\.[0-9]+)?s$`}
	case "google.protobuf.Struct":
		return map[string]any{
			"type":        "object",
			"description": "An extension slot. Its shape is the JSON Schema the catalogue registers for it; numbers in it must be integers a float64 holds exactly.",
		}
	case "google.protobuf.Value":
		return map[string]any{"description": "Any JSON value."}
	case "google.protobuf.ListValue":
		return map[string]any{"type": "array"}
	}
	name := defName(md.FullName())
	if !g.seen[name] {
		g.seen[name] = true
		// Reserve the name before walking, so a message that contains itself
		// references the entry being built rather than recursing forever.
		g.defs[name] = map[string]any{}
		g.defs[name] = g.message(md)
	}
	return map[string]any{"$ref": "#/$defs/" + name}
}

func (g *generator) enum(ed protoreflect.EnumDescriptor) map[string]any {
	values := ed.Values()
	names := make([]string, 0, values.Len())
	for i := 0; i < values.Len(); i++ {
		names = append(names, string(values.Get(i).Name()))
	}
	out := map[string]any{"type": "string", "enum": names}
	if c := g.comments[ed.FullName()]; c != "" {
		out["description"] = c
	}
	return out
}

// Flatten turns a leading comment into one paragraph.
func Flatten(comment string) string {
	text := strings.TrimSpace(comment)
	if text == "" {
		return ""
	}
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		lines = append(lines, strings.TrimSpace(l))
	}
	return strings.TrimSpace(strings.Join(lines, " "))
}

// defName is the message's name without the package, so that a reader sees
// Actor rather than audit.v1.Actor.
func defName(full protoreflect.FullName) string {
	parts := strings.Split(string(full), ".")
	return parts[len(parts)-1]
}
