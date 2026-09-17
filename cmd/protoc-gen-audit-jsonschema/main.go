// Command protoc-gen-audit-jsonschema is the buf plugin that writes the
// record's JSON Schema.
//
// It exists as a plugin, rather than as a subcommand of the binary, for one
// reason: only a plugin sees the proto's comments. The descriptor compiled into
// a Go binary has no source information, and a schema that describes a record's
// shape without its meaning would leave the archive semantically mute for
// exactly the reader it is kept for.
package main

import (
	"fmt"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/truvity/audit/internal/schemagen"
)

const recordName = "audit.v1.Record"

func main() {
	protogen.Options{}.Run(func(gen *protogen.Plugin) error {
		comments := schemagen.Comments{}
		var root protoreflect.MessageDescriptor
		for _, f := range gen.Files {
			collect(f, comments)
			if !f.Generate {
				continue
			}
			for _, m := range f.Messages {
				if m.Desc.FullName() == recordName {
					root = m.Desc
				}
			}
		}
		if root == nil {
			return nil
		}
		b, err := schemagen.Generate(root, schemagen.ID, schemagen.Title, schemagen.Description, comments)
		if err != nil {
			return fmt.Errorf("%s: %w", recordName, err)
		}
		out := gen.NewGeneratedFile(schemagen.FileName, "")
		_, err = out.Write(b)
		return err
	})
}

// collect gathers the leading comments of every message, field and enum in a
// file, by full name, including nested ones.
func collect(f *protogen.File, into schemagen.Comments) {
	var messages func([]*protogen.Message)
	messages = func(ms []*protogen.Message) {
		for _, m := range ms {
			into[m.Desc.FullName()] = schemagen.Flatten(string(m.Comments.Leading))
			for _, fd := range m.Fields {
				into[fd.Desc.FullName()] = schemagen.Flatten(string(fd.Comments.Leading))
			}
			for _, e := range m.Enums {
				into[e.Desc.FullName()] = schemagen.Flatten(string(e.Comments.Leading))
			}
			messages(m.Messages)
		}
	}
	messages(f.Messages)
	for _, e := range f.Enums {
		into[e.Desc.FullName()] = schemagen.Flatten(string(e.Comments.Leading))
	}
}
