// Package wire is how this repository's services speak JSON.
//
// The reference promises snake_case JSON — `occurred_at`, `tenant_id` — and the
// archive has always been written that way. The services were not: connect-go's
// default JSON codec writes lowerCamelCase, so a response said `occurredAt`
// where the reference, the archive and every exported file say `occurred_at`.
// It went unnoticed because reading is lenient — protojson accepts either
// spelling on the way in — and because Go clients speak binary protobuf, where
// names do not exist. Only a JSON reader saw it: a browser, the viewer, curl.
//
// HandlerOptions is the fix, and every handler in this repository applies it
// before the caller's own options, so that none of them is the one that forgot
// and a deployment can still override it deliberately.
package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// HandlerOptions make a handler write snake_case JSON, under both names a
// Connect client may ask for JSON by.
func HandlerOptions() []connect.HandlerOption {
	return []connect.HandlerOption{
		connect.WithCodec(Codec{name: "json"}),
		connect.WithCodec(Codec{name: "json; charset=utf-8"}),
	}
}

// Codec is protojson with the proto's own field names on output.
//
// Input is unchanged from connect-go's own codec: either spelling is accepted
// and unknown fields are discarded, so that a client one schema version ahead
// is not refused. Only what this side writes is fixed.
type Codec struct{ name string }

var (
	marshal   = protojson.MarshalOptions{UseProtoNames: true}
	unmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}
)

// Name implements connect.Codec.
func (c Codec) Name() string {
	if c.name == "" {
		return "json"
	}
	return c.name
}

// Marshal implements connect.Codec.
func (Codec) Marshal(message any) ([]byte, error) {
	m, ok := message.(proto.Message)
	if !ok {
		return nil, notProto(message)
	}
	return marshal.Marshal(m)
}

// MarshalAppend implements connect's optional append codec.
func (Codec) MarshalAppend(dst []byte, message any) ([]byte, error) {
	m, ok := message.(proto.Message)
	if !ok {
		return nil, notProto(message)
	}
	return marshal.MarshalAppend(dst, m)
}

// MarshalStable implements connect's optional stable codec, which GET requests
// use as a cache key. protojson's whitespace is not stable, so it is compacted.
func (c Codec) MarshalStable(message any) ([]byte, error) {
	out, err := c.Marshal(message)
	if err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, out); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

// IsBinary implements connect's optional binary marker.
func (Codec) IsBinary() bool { return false }

// Unmarshal implements connect.Codec.
func (Codec) Unmarshal(data []byte, message any) error {
	m, ok := message.(proto.Message)
	if !ok {
		return notProto(message)
	}
	if len(data) == 0 {
		return errors.New("wire: an empty body is not a JSON object")
	}
	if err := unmarshal.Unmarshal(data, m); err != nil {
		return fmt.Errorf("wire: unmarshal into %T: %w", message, err)
	}
	return nil
}

func notProto(message any) error {
	return fmt.Errorf("wire: %T is not a protobuf message", message)
}
