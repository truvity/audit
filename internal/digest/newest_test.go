package digest_test

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/audit/internal/digest"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
)

func TestNewestEnd(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 14, 20, 0, 0, time.UTC)
	put := func(s store.Store, profile string, start time.Time) {
		t.Helper()
		if err := s.Put(ctx, store.Object{Key: digest.Key(profile, start), Body: []byte("{}")}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("the hour that has just closed", func(t *testing.T) {
		s := storetest.NewMemory()
		put(s, "security", time.Date(2026, 10, 3, 13, 0, 0, 0, time.UTC))
		end, ok, err := digest.NewestEnd(ctx, s, "security", now, 24*time.Hour)
		if err != nil || !ok || !end.Equal(time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC)) {
			t.Fatalf("got %v %v %v", end, ok, err)
		}
	})
	t.Run("a chain that stopped three hours ago", func(t *testing.T) {
		s := storetest.NewMemory()
		put(s, "security", time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC))
		end, ok, _ := digest.NewestEnd(ctx, s, "security", now, 24*time.Hour)
		if !ok || !end.Equal(time.Date(2026, 10, 3, 11, 0, 0, 0, time.UTC)) {
			t.Fatalf("got %v %v", end, ok)
		}
	})
	t.Run("another profile's digest does not count, and none within the lookback is not found", func(t *testing.T) {
		s := storetest.NewMemory()
		put(s, "billing", time.Date(2026, 10, 3, 13, 0, 0, 0, time.UTC))
		if _, ok, err := digest.NewestEnd(ctx, s, "security", now, 24*time.Hour); ok || err != nil {
			t.Fatalf("found %v, %v", ok, err)
		}
	})
}
