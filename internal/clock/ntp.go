// Package clock measures how far this machine's clock is from UTC.
//
// ETSI EN 319 401 §7.10 asks that the clock be synchronised with UTC and that
// the synchronisation be recorded. Recording it is the part that concerns this
// repository: an audit trail whose timestamps nobody ever checked is one whose
// ordering an auditor has to take on faith, and "recorded" means there is an
// event saying it was checked and by how much it was out.
//
// What this does not do is set the clock. That belongs to whatever runs the
// machine, and a component that both sets the time and records the times of
// things would be marking its own paper.
package clock

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

// epochOffset is the seconds between the NTP epoch of 1900 and the Unix epoch.
const epochOffset = 2208988800

// packetSize is an NTP packet without any extension fields.
const packetSize = 48

// Reading is one comparison against one reference.
type Reading struct {
	// Source is the reference that answered.
	Source string
	// Offset is the correction this clock needs to match the reference: the
	// reference's time minus this clock's. Positive means this clock is behind
	// and should move forward. That is the sign RFC 5905 §8 gives it, and the
	// sign matters more than most: a recorded offset whose direction a reader
	// has to guess says nothing about whether events were stamped early or late.
	Offset time.Duration
	// Delay is the round trip less the time the server held the request. Of
	// several readings the one with the shortest round trip is the most
	// trustworthy, because the offset's error is bounded by half the delay.
	Delay time.Duration
	// Stratum is how far the reference is from a reference clock.
	Stratum uint8
}

// Query asks one server for the time and returns what it says about this clock.
//
// It is SNTP: one request, one reply, the four timestamps of RFC 5905 §8. That
// is the whole of what is needed to say how far out a clock is, and a component
// that is not disciplining a clock needs no more.
func Query(ctx context.Context, server string, timeout time.Duration) (Reading, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	address := server
	if _, _, err := net.SplitHostPort(server); err != nil {
		address = net.JoinHostPort(server, "123")
	}

	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "udp", address)
	if err != nil {
		return Reading{}, fmt.Errorf("clock: %s: %w", server, err)
	}
	defer conn.Close() //nolint:errcheck // a datagram socket has nothing to flush

	deadline := time.Now().Add(timeout)
	if at, ok := ctx.Deadline(); ok && at.Before(deadline) {
		deadline = at
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return Reading{}, fmt.Errorf("clock: %s: %w", server, err)
	}

	request := make([]byte, packetSize)
	// Leap indicator 0, version 4, mode 3 (client).
	request[0] = 0<<6 | 4<<3 | 3

	// The transmit timestamp is echoed back, which is how a reply is matched to
	// its request. It is read from the clock being measured, which is the point.
	sent := time.Now()
	binary.BigEndian.PutUint64(request[40:48], timestamp(sent))

	if _, err := conn.Write(request); err != nil {
		return Reading{}, fmt.Errorf("clock: %s: %w", server, err)
	}
	reply := make([]byte, packetSize)
	n, err := conn.Read(reply)
	received := time.Now()
	if err != nil {
		return Reading{}, fmt.Errorf("clock: %s: %w", server, err)
	}
	if n < packetSize {
		return Reading{}, fmt.Errorf("clock: %s: answered %d bytes, want at least %d", server, n, packetSize)
	}

	if mode := reply[0] & 0x7; mode != 4 {
		return Reading{}, fmt.Errorf("clock: %s: answered in mode %d, want a server's 4", server, mode)
	}
	stratum := reply[1]
	if stratum == 0 {
		// Stratum 0 carries a four-character reason in the reference field
		// rather than a time. Taking it for a time would read as 1900.
		return Reading{}, fmt.Errorf("clock: %s: refused: %q", server, trim(reply[12:16]))
	}
	if stratum > 15 {
		return Reading{}, fmt.Errorf("clock: %s: stratum %d is not synchronised", server, stratum)
	}
	if echoed := binary.BigEndian.Uint64(reply[24:32]); echoed != timestamp(sent) {
		return Reading{}, fmt.Errorf("clock: %s: the reply answers a different request", server)
	}

	serverReceived := at(binary.BigEndian.Uint64(reply[32:40]))
	serverSent := at(binary.BigEndian.Uint64(reply[40:48]))
	if serverSent.IsZero() || serverReceived.IsZero() {
		return Reading{}, fmt.Errorf("clock: %s: answered without a time", server)
	}

	// RFC 5905 §8: the offset is the mean of what each side thinks the other is
	// out by, which cancels a symmetric path delay.
	offset := (serverReceived.Sub(sent) + serverSent.Sub(received)) / 2
	delay := received.Sub(sent) - serverSent.Sub(serverReceived)
	if delay < 0 {
		delay = 0
	}
	return Reading{Source: server, Offset: offset, Delay: delay, Stratum: stratum}, nil
}

// Best asks every server and returns the reading with the shortest round trip,
// because the error in an offset is bounded by half the delay that carried it.
//
// A server that does not answer is not a failure: the point of naming several
// is that one may be unreachable. All of them failing is.
func Best(ctx context.Context, servers []string, timeout time.Duration) (Reading, []error, error) {
	if len(servers) == 0 {
		return Reading{}, nil, errors.New("clock: name at least one time reference")
	}
	var best Reading
	var found bool
	var failures []error
	for _, server := range servers {
		reading, err := Query(ctx, server, timeout)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !found || reading.Delay < best.Delay {
			best, found = reading, true
		}
	}
	if !found {
		return Reading{}, failures, fmt.Errorf("clock: no time reference answered (%d tried)", len(servers))
	}
	return best, failures, nil
}

// timestamp is a moment in the NTP 32.32 fixed-point form.
func timestamp(t time.Time) uint64 {
	seconds := uint64(t.Unix() + epochOffset)
	// The fraction is the sub-second part over a second, in 2^-32 units.
	fraction := uint64(t.Nanosecond()) << 32 / uint64(time.Second)
	return seconds<<32 | fraction
}

// at is the moment an NTP timestamp names.
func at(v uint64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	seconds := int64(v>>32) - epochOffset
	nanoseconds := int64(v&0xFFFFFFFF) * int64(time.Second) >> 32
	return time.Unix(seconds, nanoseconds).UTC()
}

// trim reads the printable part of a four-character reference identifier.
func trim(b []byte) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			out = append(out, c)
		}
	}
	return string(out)
}
