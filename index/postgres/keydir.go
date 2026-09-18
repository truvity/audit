package postgres

import (
	"context"
	"errors"
	"fmt"
)

// ErrKeyDirectory is a writer arriving with a key directory other than the one
// this deployment's writers share.
var ErrKeyDirectory = errors.New("postgres: this writer's key directory is not the deployment's")

// BindKeyDirectory registers a key directory's identity as the deployment's,
// or checks that it already is.
//
// The first writer to start registers its directory. Every later one, on every
// start, must bring the same: a writer with its own directory would mint its
// own keys and hand the same person a second pseudonym, and a directory that
// was lost and recreated would do the same to every tenant at once. Refusing is
// the only safe answer, because pseudonyms minted from the wrong keys are in
// the archive under a lock the moment they are written.
func BindKeyDirectory(ctx context.Context, db DB, id string) error {
	if id == "" {
		return errors.New("postgres: a key directory needs an identity to bind")
	}
	if _, err := db.Exec(ctx,
		`insert into audit_key_directory (directory_id) values ($1) on conflict (singleton) do nothing`,
		id); err != nil {
		return fmt.Errorf("postgres: registering the key directory: %w", err)
	}
	var bound string
	if err := db.QueryRow(ctx, `select directory_id from audit_key_directory`).Scan(&bound); err != nil {
		return fmt.Errorf("postgres: reading the key directory: %w", err)
	}
	if bound != id {
		return fmt.Errorf(
			"%w: this deployment's writers share directory %s and this one has %s. Either the "+
				"directory is not mounted from shared storage — every replica must see the same one — "+
				"or it was lost and recreated, which gives every person a new pseudonym. If the old "+
				"directory is truly gone and that is accepted, see the runbook, \"The key directory "+
				"changed\"", ErrKeyDirectory, bound, id)
	}
	return nil
}
