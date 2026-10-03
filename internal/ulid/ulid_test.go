package ulid

import (
	"sort"
	"testing"
	"time"
)

func TestMonotonicWithinAMillisecondAndAcrossAClockStep(t *testing.T) {
	var g Generator
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 1000; i++ {
		// Every fifth call the clock steps back; the ULIDs must not.
		moment := at
		if i%5 == 4 {
			moment = at.Add(-time.Hour)
		}
		id, when, err := g.New(moment)
		if err != nil {
			t.Fatal(err)
		}
		if !Valid(id) {
			t.Fatalf("%q is not valid", id)
		}
		if when.Before(at) {
			t.Fatalf("the clock stepped back to %s", when)
		}
		ids = append(ids, id)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("ULIDs made in sequence do not sort in sequence")
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] == ids[i-1] {
			t.Fatal("two ULIDs are equal")
		}
	}
}

func TestTimeRoundTrips(t *testing.T) {
	var g Generator
	at := time.Date(2026, 10, 3, 12, 34, 56, 789_000_000, time.UTC)
	id, _, err := g.New(at)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Time(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(at) {
		t.Fatalf("Time(%s) = %s, want %s", id, got, at)
	}
}

func TestLaterMomentSortsLater(t *testing.T) {
	var a, b Generator
	x, _, _ := a.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	y, _, _ := b.New(time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC))
	if x >= y {
		t.Fatalf("%s should sort before %s", x, y)
	}
}

func TestValidRefusesWhatIsNotOne(t *testing.T) {
	for _, s := range []string{"", "short", "01ARZ3NDEKTSV4RRFFQ69G5FAU", "81ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAVX"} {
		if Valid(s) {
			t.Errorf("%q should not be valid", s)
		}
	}
	if !Valid("01ARZ3NDEKTSV4RRFFQ69G5FAV") {
		t.Error("the canonical example should be valid")
	}
}
