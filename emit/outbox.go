package emit

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/truvity/audit/record"
)

// Outbox is a durable local store for records that must not be lost but need
// not delay the request that caused them.
//
// It is the middle ground between the other two delivery modes. Block makes an
// outage of the trail an outage of the service; best effort makes it invisible.
// An outbox makes it a delay: the record is on disk before the request
// completes, and reaches the sink when the sink is there again.
type Outbox interface {
	// Append stores a record durably. It returns only once the record would
	// survive the process being killed.
	Append(ctx context.Context, r *record.Record) error
	// Deliver hands stored batches to write, oldest first, and forgets each
	// batch write accepts. It stops at the first batch write refuses, so that
	// order is kept and nothing is skipped.
	Deliver(ctx context.Context, write func(context.Context, []*record.Record) error) error
	// Pending is how many records are waiting, for a deployment to alert on.
	Pending() (int, error)
	// Close releases the store.
	Close() error
}

// FileOutbox is an Outbox on the local filesystem.
//
// Records are appended to an open segment and flushed to the disk before Append
// returns. A segment is sealed by renaming it, so a crash can leave a sealed
// segment or an open one but never a half-named file; both are delivered on the
// next pass. Delivering a sealed segment deletes it only after the sink has
// taken it, so a crash mid-delivery costs a repeat rather than a loss. The
// writer deduplicates by record identifier, which is what makes that repeat
// harmless.
type FileOutbox struct {
	dir string

	mu     sync.Mutex
	file   *os.File
	writer *bufio.Writer
	count  int
	closed bool
}

const (
	openSegment    = "open.ndjson"
	sealedPrefix   = "sealed-"
	sealedSuffix   = ".ndjson"
	segmentRecords = 1000
)

// OpenFileOutbox opens or creates an outbox under dir. Whatever a previous
// process left behind is picked up.
func OpenFileOutbox(dir string) (*FileOutbox, error) {
	if dir == "" {
		return nil, errors.New("emit: an outbox needs a directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("emit: outbox %s: %w", dir, err)
	}
	o := &FileOutbox{dir: dir}
	// Anything the last process left open is sealed now, so this process starts
	// on a segment of its own and the old records are delivered first.
	if err := o.sealIfPresent(); err != nil {
		return nil, err
	}
	return o, nil
}

// Append implements Outbox.
func (o *FileOutbox) Append(_ context.Context, r *record.Record) error {
	line, err := record.Canonical(r)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return errors.New("emit: the outbox is closed")
	}
	if o.file == nil {
		f, err := os.OpenFile(o.path(openSegment), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("emit: outbox: %w", err)
		}
		o.file, o.writer, o.count = f, bufio.NewWriter(f), 0
	}
	if _, err := o.writer.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("emit: outbox: %w", err)
	}
	if err := o.writer.Flush(); err != nil {
		return fmt.Errorf("emit: outbox: %w", err)
	}
	// Without this the record is in the kernel's cache and not on the disk,
	// and a power loss would take exactly the records the outbox exists to
	// keep.
	if err := o.file.Sync(); err != nil {
		return fmt.Errorf("emit: outbox: %w", err)
	}
	o.count++
	if o.count >= segmentRecords {
		return o.seal()
	}
	return nil
}

// Deliver implements Outbox.
func (o *FileOutbox) Deliver(ctx context.Context, write func(context.Context, []*record.Record) error) error {
	o.mu.Lock()
	if err := o.seal(); err != nil {
		o.mu.Unlock()
		return err
	}
	o.mu.Unlock()

	sealed, err := o.sealed()
	if err != nil {
		return err
	}
	for _, name := range sealed {
		records, err := o.read(name)
		if err != nil {
			return err
		}
		if len(records) > 0 {
			if err := write(ctx, records); err != nil {
				// Stop here. Delivering the next segment would put later
				// records in front of these, and an audit trail that arrives
				// out of order is one nobody can reason about.
				return err
			}
		}
		if err := os.Remove(o.path(name)); err != nil {
			return fmt.Errorf("emit: outbox: %w", err)
		}
	}
	return nil
}

// Pending implements Outbox.
func (o *FileOutbox) Pending() (int, error) {
	o.mu.Lock()
	open := o.count
	o.mu.Unlock()
	sealed, err := o.sealed()
	if err != nil {
		return 0, err
	}
	total := open
	for _, name := range sealed {
		records, err := o.read(name)
		if err != nil {
			return 0, err
		}
		total += len(records)
	}
	return total, nil
}

// Close implements Outbox. What is still open is sealed, so the next process
// finds it.
func (o *FileOutbox) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	return o.seal()
}

// seal closes the open segment and renames it, so that it is complete and
// named at the same instant. The caller holds the lock.
func (o *FileOutbox) seal() error {
	if o.file == nil {
		return nil
	}
	if err := o.writer.Flush(); err != nil {
		return fmt.Errorf("emit: outbox: %w", err)
	}
	if err := o.file.Sync(); err != nil {
		return fmt.Errorf("emit: outbox: %w", err)
	}
	if err := o.file.Close(); err != nil {
		return fmt.Errorf("emit: outbox: %w", err)
	}
	o.file, o.writer = nil, nil
	empty := o.count == 0
	o.count = 0
	if empty {
		return os.Remove(o.path(openSegment))
	}
	return o.rename()
}

// sealIfPresent seals what a previous process left open.
func (o *FileOutbox) sealIfPresent() error {
	if _, err := os.Stat(o.path(openSegment)); err != nil {
		return nil //nolint:nilerr // nothing left open is the ordinary case
	}
	return o.rename()
}

func (o *FileOutbox) rename() error {
	name := fmt.Sprintf("%s%019d%s", sealedPrefix, time.Now().UTC().UnixNano(), sealedSuffix)
	if err := os.Rename(o.path(openSegment), o.path(name)); err != nil {
		return fmt.Errorf("emit: outbox: %w", err)
	}
	return nil
}

// sealed lists the sealed segments, oldest first. The names carry the time they
// were sealed, so their order is the order the records were written.
func (o *FileOutbox) sealed() ([]string, error) {
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		return nil, fmt.Errorf("emit: outbox: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), sealedPrefix) && strings.HasSuffix(e.Name(), sealedSuffix) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// read decodes a segment. A line that does not decode is a record this build
// cannot deliver, and stopping on it would wedge every record behind it, so it
// is skipped and the rest go on.
func (o *FileOutbox) read(name string) ([]*record.Record, error) {
	f, err := os.Open(o.path(name))
	if err != nil {
		return nil, fmt.Errorf("emit: outbox: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only

	var out []*record.Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), record.Default.MaxBytes+1<<10)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r record.Record
		if err := record.Unmarshal(line, &r); err != nil {
			continue
		}
		out = append(out, &r)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("emit: outbox %s: %w", name, err)
	}
	return out, nil
}

func (o *FileOutbox) path(name string) string { return filepath.Join(o.dir, name) }
