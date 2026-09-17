package cli_test

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// reference answers as a time server with a clock a given amount ahead.
func reference(t *testing.T, ahead time.Duration) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 64)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 48 {
				continue
			}
			reply := make([]byte, 48)
			reply[0] = 0<<6 | 4<<3 | 4
			reply[1] = 2
			copy(reply[24:32], buf[40:48])
			now := time.Now().Add(ahead)
			seconds := uint64(now.Unix() + 2208988800)
			fraction := uint64(now.Nanosecond()) << 32 / uint64(time.Second)
			stamp := seconds<<32 | fraction
			binary.BigEndian.PutUint64(reply[32:40], stamp)
			binary.BigEndian.PutUint64(reply[40:48], stamp)
			_, _ = conn.WriteToUDP(reply, from)
		}
	}()
	return conn.LocalAddr().String()
}

// collector stands in for the writer.
type collector struct{ records []*record.Record }

func (c *collector) Write(_ context.Context, req *sink.Request) (*sink.Result, error) {
	c.records = append(c.records, req.Records...)
	return &sink.Result{Accepted: len(req.Records)}, nil
}

func clockRun(t *testing.T, s sink.Sink, servers ...string) cli.ClockSync {
	t.Helper()
	common, err := catalogue.Common()
	if err != nil {
		t.Fatal(err)
	}
	return cli.ClockSync{
		Sink: s, Catalogue: common, Servers: servers,
		Timeout: time.Second, Instance: "clock-1", Version: "test",
		Out: &strings.Builder{},
	}
}

// The requirement is that the synchronisation is recorded, so the event is the
// deliverable and it has to carry what was measured.
func TestClockSyncRecordsWhatItMeasured(t *testing.T) {
	ahead := 2 * time.Second
	server := reference(t, ahead)
	into := &collector{}

	report, err := clockRun(t, into, server).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Recorded {
		t.Fatal("the check ran and nothing in the trail says so")
	}
	if len(into.records) != 1 {
		t.Fatalf("%d records", len(into.records))
	}
	r := into.records[0]
	if r.GetAction() != "audit.clock.synchronised" {
		t.Fatalf("action %q", r.GetAction())
	}
	if r.GetTenantId() != record.TenantPlatform {
		t.Fatalf("tenant %q, want the platform's own", r.GetTenantId())
	}
	data := r.GetData().AsMap()
	if got := data["source"]; got != server {
		t.Fatalf("source %v, want %s", got, server)
	}
	// The offset is the correction this clock needs, so a reference that is
	// ahead gives a positive one.
	offset, ok := data["offset_ms"].(float64)
	if !ok {
		t.Fatalf("offset_ms is %T, want a number", data["offset_ms"])
	}
	if offset < 1500 || offset > 2500 {
		t.Fatalf("offset_ms %v, want about %d", offset, ahead.Milliseconds())
	}
	if report.OffsetMS != int64(offset) {
		t.Fatalf("the report says %d and the record says %v", report.OffsetMS, offset)
	}
}

// A clock further out than the deployment allows is a failure the scheduled run
// reports, and the reading is recorded anyway: an hour whose timestamps are
// suspect is the hour an auditor most wants the measurement from.
func TestClockSyncFailsOnADriftedClockAndRecordsItAnyway(t *testing.T) {
	server := reference(t, 30*time.Second)
	into := &collector{}
	run := clockRun(t, into, server)
	run.MaxOffset = time.Second

	report, err := run.Run(context.Background())
	if err == nil {
		t.Fatal("a clock outside the tolerance must fail the run")
	}
	if !strings.Contains(err.Error(), "more than") {
		t.Errorf("the error should say what was allowed: %v", err)
	}
	if report.Within {
		t.Fatal("the report says the offset was within tolerance")
	}
	if len(into.records) != 1 {
		t.Fatal("a drifted clock was not recorded, which is when it matters most")
	}
}

// Zero means record whatever is found and never fail, for a deployment that
// would rather see the number than have a job go red.
func TestClockSyncWithNoToleranceOnlyRecords(t *testing.T) {
	server := reference(t, 30*time.Second)
	run := clockRun(t, &collector{}, server)
	run.MaxOffset = 0
	report, err := run.Run(context.Background())
	if err != nil {
		t.Fatalf("with no tolerance set, any offset is accepted: %v", err)
	}
	if !report.Within {
		t.Fatal("with no tolerance set, the offset is within it by definition")
	}
}

// Without a writer the check still runs and says plainly that nothing recorded
// it, because a check whose result is not in the trail has not met the
// requirement however good the offset was.
func TestClockSyncWithoutAWriterSaysItRecordedNothing(t *testing.T) {
	server := reference(t, 0)
	run := clockRun(t, nil, server)
	run.Sink = nil
	report, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Recorded {
		t.Fatal("there was no writer and the report claims the reading was recorded")
	}
}

// If no reference answers, the clock was not checked, and saying it was would
// be the one outcome worse than a failed job.
func TestClockSyncFailsWhenNothingAnswers(t *testing.T) {
	into := &collector{}
	run := clockRun(t, into, "127.0.0.1:1")
	run.Timeout = 200 * time.Millisecond
	if _, err := run.Run(context.Background()); err == nil {
		t.Fatal("no reference answered, so the clock was not checked")
	}
	if len(into.records) != 0 {
		t.Fatal("a check that did not happen was recorded as though it had")
	}
}
