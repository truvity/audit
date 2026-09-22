package emit_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/truvity/audit/emit"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// childEnv tells a re-run of this test binary to be the process that gets
// killed, and where to write what the sink accepted.
const childEnv = "AUDIT_KILLED_CHILD"

// A record the sink accepted survives a sink that was failing a moment before:
// an async batch is retried until it is taken, so nothing is given up for a
// sink that comes back.
func TestAsyncRetriesUntilTheSinkTakesIt(t *testing.T) {
	var attempts atomic.Int32
	store := &sink.Memory{}
	flaky := sink.Func(func(ctx context.Context, req *sink.Request) (*sink.Result, error) {
		if attempts.Add(1) <= 3 {
			return nil, errors.New("the sink is unreachable")
		}
		return store.Write(ctx, req)
	})

	var mu sync.Mutex
	var dropped int
	e, err := emit.New(emit.Options{
		Source: "shop", Catalogue: shop(t), Sink: flaky,
		Flush: time.Millisecond, Retry: time.Millisecond,
		Hooks: emit.Hooks{OnDropped: func(*record.Record, string) {
			mu.Lock()
			dropped++
			mu.Unlock()
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := e.Record(context.Background(), viewed()); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	if store.Len() != 3 {
		t.Errorf("the sink holds %d records, want all 3: a failing sink must be retried, not given up", store.Len())
	}
	if attempts.Load() < 4 {
		t.Errorf("%d attempts: the batch should have been retried", attempts.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if dropped != 0 {
		t.Errorf("%d records given up although the sink came back", dropped)
	}
}

// A process killed outright loses exactly what was still queued, and nothing
// it was told was durable.
//
// The child records under both deliveries against a sink that appends every
// accepted id to a file. A block record is written before its call returns, so
// it is in the file; an async record is still in the queue, because the child
// holds the sink open until it is killed. SIGKILL means no shutdown runs: no
// Close, no flush, no deferred anything.
func TestAKillLosesTheQueueAndNothingElse(t *testing.T) {
	if path := os.Getenv(childEnv); path != "" {
		killedChild(t, path)
		return
	}
	path := filepath.Join(t.TempDir(), "accepted")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAKillLosesTheQueueAndNothingElse$", "-test.count=1")
	cmd.Env = append(os.Environ(), childEnv+"="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	var blocked, queued []string
	lines := bufio.NewScanner(out)
	for lines.Scan() {
		line := lines.Text()
		if id, ok := strings.CutPrefix(line, "blocked "); ok {
			blocked = append(blocked, id)
		}
		if id, ok := strings.CutPrefix(line, "queued "); ok {
			queued = append(queued, id)
		}
		if line == "ready" {
			break
		}
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if len(blocked) == 0 || len(queued) == 0 {
		t.Fatalf("the child recorded %d blocking and %d queued, so nothing was tested", len(blocked), len(queued))
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	accepted := map[string]bool{}
	for _, id := range strings.Fields(string(raw)) {
		accepted[id] = true
	}
	for _, id := range blocked {
		if !accepted[id] {
			t.Errorf("%s was acknowledged to the application and is not in the sink", id)
		}
	}
	for _, id := range queued {
		if accepted[id] {
			t.Errorf("%s was still queued and reached the sink anyway, so the test proves nothing", id)
		}
	}
}

// killedChild records under both deliveries and waits to be killed. Its async
// records never leave it: the sink it writes them through blocks forever.
func killedChild(t *testing.T, path string) {
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	held := make(chan struct{})
	appendOnly := sink.Func(func(_ context.Context, req *sink.Request) (*sink.Result, error) {
		if req.Delivery == sink.Async {
			<-held // never returns: the records stay in the emitter
		}
		for _, r := range req.Records {
			if _, err := fmt.Fprintln(file, r.GetId()); err != nil {
				return nil, err
			}
		}
		if err := file.Sync(); err != nil {
			return nil, err
		}
		return &sink.Result{Accepted: len(req.Records)}, nil
	})

	e, err := emit.New(emit.Options{
		Source: "shop", Catalogue: shop(t), Sink: appendOnly,
		Queue: 64, Batch: 1, Flush: time.Millisecond, Retry: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r := placed() // declared block
		if err := e.Record(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("blocked %s\n", r.GetId())
	}
	for i := 0; i < 3; i++ {
		r := viewed() // declared async
		if err := e.Record(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("queued %s\n", r.GetId())
	}
	fmt.Println("ready")
	_ = os.Stdout.Sync()
	time.Sleep(time.Minute)
}
