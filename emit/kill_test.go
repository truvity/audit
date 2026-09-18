package emit_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/truvity/audit/emit"
	"github.com/truvity/audit/sink"
)

// childEnv tells a re-run of this test binary to be the process that gets
// killed.
const childEnv = "AUDIT_OUTBOX_KILLED_CHILD"

// What an outbox accepted survives the process being killed outright — not
// closed, not cancelled: SIGKILL, with nothing flushed on the way out.
//
// The other outbox tests simulate a crash by closing or by failing a delivery,
// which still runs the emitter's own shutdown. This one runs the emitter in a
// child process, has it record while the sink is unreachable, kills it
// without warning, and requires a fresh process on the same directory to
// deliver every record the child was told was recorded.
func TestOutboxSurvivesAKill(t *testing.T) {
	if dir := os.Getenv(childEnv); dir != "" {
		killedChild(t, dir)
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOutboxSurvivesAKill$", "-test.count=1")
	cmd.Env = append(os.Environ(), childEnv+"="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Read what the child says it recorded, up to the moment it says it is
	// done; then kill it.
	var ids []string
	lines := bufio.NewScanner(out)
	for lines.Scan() {
		line := lines.Text()
		if id, ok := strings.CutPrefix(line, "recorded "); ok {
			ids = append(ids, id)
		}
		if line == "ready" {
			break
		}
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if len(ids) == 0 {
		t.Fatal("the child recorded nothing, so nothing was tested")
	}

	up := &sink.Memory{}
	next, box := outboxEmitter(t, dir, up, emit.Hooks{})
	if pending, err := box.Pending(); err != nil || pending < len(ids) {
		t.Fatalf("after the kill %d pending (%v), want at least the %d recorded", pending, err, len(ids))
	}
	if err := next.Close(); err != nil { // closing drains once
		t.Fatal(err)
	}
	delivered := map[string]bool{}
	for _, r := range up.Records() {
		delivered[r.GetId()] = true
	}
	for _, id := range ids {
		if !delivered[id] {
			t.Errorf("%s was recorded before the kill and never delivered", id)
		}
	}
}

// killedChild records into an outbox whose sink is down, says so, and waits to
// be killed.
func killedChild(t *testing.T, dir string) {
	down := &sink.Memory{Fail: errors.New("the store is unreachable")}
	e, _ := outboxEmitter(t, dir, down, emit.Hooks{})
	for i := 0; i < 5; i++ {
		r := shipped()
		if err := e.Record(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("recorded %s\n", r.GetId())
	}
	fmt.Println("ready")
	_ = os.Stdout.Sync()
	time.Sleep(time.Minute)
}
