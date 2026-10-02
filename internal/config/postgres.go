package config

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolConfig is the connection pool the database block describes. The password
// is read from the environment variable the file names, here and nowhere
// else, and set on the connection rather than spliced into the URL, so that
// it needs no escaping and appears in no string that might be logged.
func (p Postgres) PoolConfig() (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(p.URL)
	if err != nil {
		// ParseConfig's error quotes the URL. It carries no password, but
		// an error is logged: say which key, not what it held.
		return nil, errors.New("database.url is not a PostgreSQL connection URL")
	}
	if p.PasswordEnv != "" {
		password, err := Secret(p.PasswordEnv)
		if err != nil {
			return nil, fmt.Errorf("database.passwordEnv: %w", err)
		}
		cfg.ConnConfig.Password = password
	}
	if p.MaxConnections > 0 {
		cfg.MaxConns = int32(p.MaxConnections)
	}
	return cfg, nil
}
