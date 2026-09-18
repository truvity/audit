package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/pgtest"
)

func TestTheFirstKeyDirectoryIsTheDeploymentsAndNoOtherIsTaken(t *testing.T) {
	db := pgtest.Open(t)
	ctx := context.Background()
	if err := postgres.BindKeyDirectory(ctx, db, "dir-a"); err != nil {
		t.Fatalf("the first writer: %v", err)
	}
	// Every restart of a writer sharing the directory binds again.
	if err := postgres.BindKeyDirectory(ctx, db, "dir-a"); err != nil {
		t.Fatalf("the same directory again: %v", err)
	}
	// A replica with its own directory, or a directory recreated after loss.
	if err := postgres.BindKeyDirectory(ctx, db, "dir-b"); !errors.Is(err, postgres.ErrKeyDirectory) {
		t.Fatalf("another directory was accepted: %v", err)
	}
}
