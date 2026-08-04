package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

type DB struct {
	*sql.DB
}

func Open(databaseURL string) (*DB, error) {
	raw, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	raw.SetMaxOpenConns(20)
	raw.SetMaxIdleConns(10)
	raw.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := raw.PingContext(ctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return &DB{DB: raw}, nil
}
