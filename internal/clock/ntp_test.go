package clock_test

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/internal/clock"
)

const epochOffset = 2208988800

// server answers as an NTP server would, with a clock a given amount ahead of
// this machine's.
type server struct {
	ahead   time.Duration
	stratum uint8
	mode    byte
	// echo false answers a request other than the one that was sent.
	echo bool
	// zeroTime answers without a transmit timestamp.
	zeroTime bool
	// hold delays the answer, which shows up as round-trip delay.
	hold time.Duration
	conn *net.UDPConn
}

func listen(t *testing.T, s *server) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	s.conn = conn
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
			if s.hold > 0 {
				time.Sleep(s.hold)
			}
			reply := make([]byte, 48)
			mode := s.mode
			if mode == 0 {
				mode = 4
			}
			reply[0] = 0<<6 | 4<<3 | mode
			reply[1] = s.stratum
			copy(reply[12:16], "RATE")

			// The originate field echoes the client's transmit timestamp.
			origin := binary.BigEndian.Uint64(buf[40:48])
			if !s.echo {
				origin++
			}
			binary.BigEndian.PutUint64(reply[24:32], origin)

			if !s.zeroTime {
				now := time.Now().Add(s.ahead)
				binary.BigEndian.PutUint64(reply[32:40], stamp(now))
				binary.BigEndian.PutUint64(reply[40:48], stamp(now))
			}
			_, _ = conn.WriteToUDP(reply, from)
		}
	}()
	return conn.LocalAddr().String()
}

func stamp(t time.Time) uint64 {
	seconds := uint64(t.Unix() + epochOffset)
	fraction := uint64(t.Nanosecond()) << 32 / uint64(time.Second)
	return seconds<<32 | fraction
}

func good(ahead time.Duration) *server {
	return &server{ahead: ahead, stratum: 2, echo: true}
}

// The offset is what the command records, so it has to be the real difference
// and in the right direction: a clock behind the reference reports a negative
// offset.
func TestQueryMeasuresTheOffsetAndItsDirection(t *testing.T) {
	for _, c := range []struct {
		name  string
		ahead time.Duration
	}{
		{"a reference ahead of this clock", 3 * time.Second},
		{"a reference behind this clock", -3 * time.Second},
		{"a clock that agrees", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			address := listen(t, good(c.ahead))
			reading, err := clock.Query(context.Background(), address, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			// The offset is the correction this clock needs, so a reference
			// that is ahead yields a positive one.
			want := c.ahead
			if diff := reading.Offset - want; diff > 250*time.Millisecond || diff < -250*time.Millisecond {
				t.Fatalf("offset %v, want about %v", reading.Offset, want)
			}
			if reading.Stratum != 2 {
				t.Fatalf("stratum %d", reading.Stratum)
			}
		})
	}
}

// Stratum 0 carries a four-character reason where the time would be. Reading it
// as a time would put the clock in 1900 and report an offset of a century.
func TestQueryRefusesAKissOfDeath(t *testing.T) {
	address := listen(t, &server{stratum: 0, echo: true})
	_, err := clock.Query(context.Background(), address, time.Second)
	if err == nil {
		t.Fatal("a stratum 0 answer carries no time and must not be read as one")
	}
	if !strings.Contains(err.Error(), "RATE") {
		t.Errorf("the refusal should carry the reason the server gave: %v", err)
	}
}

// A datagram socket will hand over whatever arrives. An answer to a request
// this process did not make is not an answer.
func TestQueryRefusesAReplyToAnotherRequest(t *testing.T) {
	address := listen(t, &server{stratum: 2, echo: false})
	if _, err := clock.Query(context.Background(), address, time.Second); err == nil {
		t.Fatal("a reply that does not echo the request must be refused")
	}
}

// An unsynchronised server says so, and its time is not one to measure against.
func TestQueryRefusesAnUnsynchronisedServer(t *testing.T) {
	address := listen(t, &server{stratum: 16, echo: true})
	if _, err := clock.Query(context.Background(), address, time.Second); err == nil {
		t.Fatal("stratum 16 is not synchronised and must be refused")
	}
}

func TestQueryRefusesAnAnswerWithNoTime(t *testing.T) {
	address := listen(t, &server{stratum: 2, echo: true, zeroTime: true})
	if _, err := clock.Query(context.Background(), address, time.Second); err == nil {
		t.Fatal("an answer without a transmit timestamp must be refused")
	}
}

// A server that does not answer must not hold the job up for longer than it was
// given.
func TestQueryGivesUp(t *testing.T) {
	// A socket nothing listens on: the port is bound and then closed.
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	address := conn.LocalAddr().String()
	_ = conn.Close()

	start := time.Now()
	if _, err := clock.Query(context.Background(), address, 200*time.Millisecond); err == nil {
		t.Fatal("want a timeout")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("gave up after %v, want about 200ms", elapsed)
	}
}

// The error in an offset is bounded by half the round trip that carried it, so
// of several answers the quickest is the one to believe.
func TestBestPrefersTheShortestRoundTrip(t *testing.T) {
	slow := listen(t, &server{ahead: 5 * time.Second, stratum: 2, echo: true, hold: 300 * time.Millisecond})
	quick := listen(t, good(time.Second))

	reading, failures, err := clock.Best(context.Background(), []string{slow, quick}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Fatalf("both answered, but %v", failures)
	}
	if reading.Source != quick {
		t.Fatalf("believed %s, want the quicker %s", reading.Source, quick)
	}
}

// Naming several references is what makes one being unreachable survivable.
// All of them being unreachable is not.
func TestBestSurvivesOneUnreachableReferenceButNotAll(t *testing.T) {
	working := listen(t, good(0))
	reading, failures, err := clock.Best(context.Background(),
		[]string{"127.0.0.1:1", working}, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("one unreachable reference must not fail the check: %v", err)
	}
	if reading.Source != working {
		t.Fatalf("believed %s", reading.Source)
	}
	if len(failures) != 1 {
		t.Fatalf("%d failures reported, want the one that did not answer", len(failures))
	}

	if _, _, err := clock.Best(context.Background(),
		[]string{"127.0.0.1:1", "127.0.0.1:2"}, 300*time.Millisecond); err == nil {
		t.Fatal("no reference answering means the clock was not checked")
	}
}
