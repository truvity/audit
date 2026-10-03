// Package recobj is the format of a record object: the one object an ingest
// batch becomes, as docs/reference/bucket-contract.md specifies it.
//
// The body is newline-delimited JSON, zstd-compressed, and each line is
//
//	{"hash":"<hex sha256>","record":{...}}
//
// where record is the profile's copy in its canonical form (RFC 8785 over the
// protobuf JSON mapping) and hash is the SHA-256 of exactly that encoding. The
// object's own SHA-256 and its record count are metadata, so that a reader
// needs a listing and a HEAD and never a body.
//
// The writer builds objects here and every reader takes them apart here, so
// the format has one definition in the code as it has one in the contract.
package recobj

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/store"
)

// ContentType and Encoding are what an object is stored as.
const (
	ContentType = "application/x-ndjson"
	Encoding    = "zstd"
)

// maxPlain bounds what a stored object may decompress to, so that a corrupt or
// hostile object cannot take a reader's memory. A batch is rolled at a few
// MiB; this is generous.
const maxPlain = 1 << 30

var (
	encoderOnce sync.Once
	encoder     *zstd.Encoder
	decoderOnce sync.Once
	decoder     *zstd.Decoder
)

func enc() *zstd.Encoder {
	encoderOnce.Do(func() {
		e, err := zstd.NewWriter(nil)
		if err != nil {
			// zstd.NewWriter fails only on a bad option, and there are none.
			panic(fmt.Sprintf("recobj: zstd: %v", err))
		}
		encoder = e
	})
	return encoder
}

func dec() *zstd.Decoder {
	decoderOnce.Do(func() {
		d, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxPlain))
		if err != nil {
			panic(fmt.Sprintf("recobj: zstd: %v", err))
		}
		decoder = d
	})
	return decoder
}

// Hash is the hash of a record: the hex SHA-256 of its canonical encoding.
func Hash(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// SHA256 is the hex SHA-256 of an object's stored bytes, which is what the
// sha256 metadata holds.
func SHA256(stored []byte) string { return Hash(stored) }

// Line is one record of an object, as the object has it.
type Line struct {
	// Hash is what the line claims for its record.
	Hash string `json:"hash"`
	// Record is the record as written, canonical.
	Record json.RawMessage `json:"record"`
}

// EncodeLine makes the line for a record already in canonical form. The bytes
// are embedded as they are, so that the hash is over what is on the line.
func EncodeLine(canonical []byte) []byte {
	out := make([]byte, 0, len(canonical)+len(`{"hash":"","record":}`)+64)
	out = append(out, `{"hash":"`...)
	out = append(out, Hash(canonical)...)
	out = append(out, `","record":`...)
	out = append(out, canonical...)
	return append(out, '}')
}

// Encode builds an object's stored bytes and metadata from its lines.
func Encode(lines [][]byte) (body []byte, metadata map[string]string) {
	var plain bytes.Buffer
	for _, line := range lines {
		plain.Write(line)
		plain.WriteByte('\n')
	}
	body = enc().EncodeAll(plain.Bytes(), nil)
	return body, Metadata(body, len(lines))
}

// Metadata is the metadata an object of these stored bytes and this many
// records carries.
func Metadata(stored []byte, count int) map[string]string {
	return map[string]string{
		store.MetaFormat: store.Format,
		store.MetaSHA256: SHA256(stored),
		store.MetaCount:  strconv.Itoa(count),
	}
}

// Decode decompresses an object and reads its lines. It does not check any
// hash: Verify does, and a reader that only wants the records does not pay
// for it.
func Decode(stored []byte) ([]Line, error) {
	plain, err := dec().DecodeAll(stored, nil)
	if err != nil {
		return nil, fmt.Errorf("recobj: decompress: %w", err)
	}
	if len(plain) == 0 {
		return nil, errors.New("recobj: the object has no lines")
	}
	if plain[len(plain)-1] != '\n' {
		return nil, errors.New("recobj: the last line has no newline")
	}
	rows := bytes.Split(plain[:len(plain)-1], []byte{'\n'})
	lines := make([]Line, 0, len(rows))
	for n, row := range rows {
		var l Line
		d := json.NewDecoder(bytes.NewReader(row))
		d.DisallowUnknownFields()
		if err := d.Decode(&l); err != nil {
			return nil, fmt.Errorf("recobj: line %d: %w", n+1, err)
		}
		if d.More() {
			return nil, fmt.Errorf("recobj: line %d: more than one value", n+1)
		}
		if l.Hash == "" || len(l.Record) == 0 {
			return nil, fmt.Errorf("recobj: line %d: a line is a hash and a record", n+1)
		}
		lines = append(lines, l)
	}
	return lines, nil
}

// Verify checks that a line's hash is the hash of its record: the record is
// put in canonical form, so a line written by another implementation with
// other whitespace still checks, and hashed.
func (l Line) Verify() error {
	canonical, err := record.CanonicalJSON(l.Record)
	if err != nil {
		return fmt.Errorf("the record is not JSON: %w", err)
	}
	if got := Hash(canonical); got != l.Hash {
		return fmt.Errorf("the line says hash %s and the record hashes to %s", l.Hash, got)
	}
	return nil
}

// Decoded reads the record of a line.
func (l Line) Decoded() (*record.Record, error) {
	var r record.Record
	if err := record.Unmarshal(l.Record, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// CheckMetadata compares what an object's metadata says with the object: the
// format, the sha256 of the stored bytes and the number of records. Every
// disagreement is returned, so that a report can say all of them.
func CheckMetadata(meta map[string]string, stored []byte, lines int) []string {
	var problems []string
	if got := meta[store.MetaFormat]; got != store.Format {
		problems = append(problems, fmt.Sprintf("format is %q, not %q", got, store.Format))
	}
	if got, want := meta[store.MetaSHA256], SHA256(stored); got != want {
		problems = append(problems, fmt.Sprintf("sha256 is %q, the object hashes to %s", got, want))
	}
	if got, want := meta[store.MetaCount], strconv.Itoa(lines); got != want {
		problems = append(problems, fmt.Sprintf("count is %q, the object has %d records", got, lines))
	}
	return problems
}
