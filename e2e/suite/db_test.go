package suite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// openDSN opens a *sql.DB against a DSN e2e/fixture/apply.sh wrote into a
// Secret, after swapping its host for the harness's forward to the box's
// one Postgres server — a Secret written for a pod inside the cluster
// names it by Service DNS, which nothing outside the cluster can resolve.
func openDSN(ctx context.Context, t *testing.T, dsn string) *sql.DB {
	t.Helper()

	forwarded, err := forwardedDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("forward the database DSN: %v", err)
	}

	db, err := sql.Open("pgx", forwarded)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("%s", wrapDBErr(fmt.Errorf("ping: %w", err)))
	}
	return db
}

// forwardedDSN resolves the box's Postgres Service through the harness and
// substitutes it for the DSN's own host, keeping everything else — the
// role, the password, the database name, sslmode — exactly as
// e2e/fixture/apply.sh wrote it.
func forwardedDSN(ctx context.Context, dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse DSN: %w", err)
	}

	raw, err := shared.cluster.ServiceURL(ctx, "postgres", "postgres", 5432)
	if err != nil {
		return "", fmt.Errorf("resolve the postgres Service: %w", err)
	}
	hostport := strings.TrimPrefix(raw, "http://")

	u.Host = hostport
	return u.String(), nil
}

// wrapDBErr enriches a Postgres connection error with the forward it went
// through, the same way every HTTP call in this suite is enriched.
func wrapDBErr(err error) error {
	if fw, ok := shared.cluster.ForwardFor("postgres", "postgres"); ok {
		return fw.Err(err)
	}
	return err
}
