package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	_ "github.com/lib/pq"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps the PostgreSQL connection pool.
type DB struct {
	Pool *sql.DB
}

// New opens a PostgreSQL connection and runs migrations.
func New(ctx context.Context, dsn string) (*DB, error) {
	pool, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("abriendo PostgreSQL: %w", err)
	}

	pool.SetMaxOpenConns(25)
	pool.SetMaxIdleConns(5)

	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("conectando a PostgreSQL: %w", err)
	}

	db := &DB{Pool: pool}

	if err := db.runMigrations(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ejecutando migraciones: %w", err)
	}

	slog.Info("postgresql_conectado")
	return db, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.Pool.Close()
}

func (db *DB) runMigrations(ctx context.Context) error {
	// Create migrations tracking table
	_, err := db.Pool.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename VARCHAR(255) PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		return fmt.Errorf("creando tabla migraciones: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("leyendo migraciones: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		// Check if already applied
		var exists bool
		err := db.Pool.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename = $1)",
			entry.Name(),
		).Scan(&exists)
		if err != nil {
			return fmt.Errorf("verificando migración %s: %w", entry.Name(), err)
		}
		if exists {
			continue
		}

		// Read and execute migration
		content, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("leyendo archivo %s: %w", entry.Name(), err)
		}

		slog.Info("aplicando_migracion", "archivo", entry.Name())

		tx, err := db.Pool.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("iniciando tx para %s: %w", entry.Name(), err)
		}

		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("ejecutando %s: %w", entry.Name(), err)
		}

		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (filename) VALUES ($1)",
			entry.Name(),
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("registrando migración %s: %w", entry.Name(), err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", entry.Name(), err)
		}
	}

	return nil
}
