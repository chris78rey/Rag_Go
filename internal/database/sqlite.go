package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps the SQLite connection pool.
type DB struct {
	Pool *sql.DB
}

// New opens a SQLite database and runs migrations.
func New(ctx context.Context, path string) (*DB, error) {
	if path == "" {
		path = "data/rag.db"
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creando directorio de datos: %w", err)
	}

	dsn := sqliteDSN(path)
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("abriendo SQLite: %w", err)
	}

	pool.SetMaxOpenConns(5)
	pool.SetMaxIdleConns(5)

	if _, err := pool.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("configurando foreign_keys: %w", err)
	}
	if _, err := pool.ExecContext(ctx, `PRAGMA journal_mode = WAL`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("configurando journal_mode: %w", err)
	}
	if _, err := pool.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("configurando busy_timeout: %w", err)
	}
	if _, err := pool.ExecContext(ctx, `PRAGMA synchronous = NORMAL`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("configurando synchronous: %w", err)
	}

	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("conectando a SQLite: %w", err)
	}

	db := &DB{Pool: pool}

	if err := db.runMigrations(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ejecutando migraciones: %w", err)
	}

	slog.Info("sqlite_conectado", "path", path)
	return db, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.Pool.Close()
}

func (db *DB) runMigrations(ctx context.Context) error {
	_, err := db.Pool.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
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

		var exists bool
		err := db.Pool.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename = ?)",
			entry.Name(),
		).Scan(&exists)
		if err != nil {
			return fmt.Errorf("verificando migración %s: %w", entry.Name(), err)
		}
		if exists {
			continue
		}

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
			"INSERT INTO schema_migrations (filename) VALUES (?)",
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

func sqliteDSN(path string) string {
	clean := filepath.ToSlash(path)
	if strings.HasPrefix(clean, "file:") {
		return clean
	}
	return "file:" + clean
}
