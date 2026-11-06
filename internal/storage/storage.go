// Package storage owns the SQLite database, the exclusive process lock and
// all row-level access. The database, WAL and SHM files live in stateDir.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store wraps the single database connection.
type Store struct {
	db *sql.DB
	// Now is injectable for tests; defaults to time.Now (UTC).
	Now func() time.Time
}

// Lock is an exclusive advisory lock on stateDir/.lock. A second process
// must fail to start rather than corrupt state.
type Lock struct{ f *os.File }

// AcquireLock takes a non-blocking exclusive flock on stateDir/.lock.
func AcquireLock(stateDir string) (*Lock, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(stateDir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another instance holds the state lock: %w", err)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	return l.f.Close()
}

// Open opens the database with WAL, foreign_keys and busy_timeout, then
// applies pending migrations in a single transaction, tracked via
// PRAGMA user_version.
func Open(stateDir string) (*Store, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(stateDir, "hsa.db")) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, Now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	for _, e := range entries {
		var num int
		if _, err := fmt.Sscanf(e.Name(), "%d_", &num); err != nil {
			return fmt.Errorf("bad migration name %s: %w", e.Name(), err)
		}
		if num <= version {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			cancel()
			return fmt.Errorf("begin migration: %w", err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			cancel()
			return fmt.Errorf("apply migration %s: %w", e.Name(), err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", num)); err != nil {
			tx.Rollback()
			cancel()
			return fmt.Errorf("bump user_version: %w", err)
		}
		if err := tx.Commit(); err != nil {
			cancel()
			return fmt.Errorf("commit migration %s: %w", e.Name(), err)
		}
		cancel()
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping verifies database availability for /healthz.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// nowString returns the current UTC RFC3339Nano timestamp.
func (s *Store) nowString() string { return s.Now().UTC().Format(time.RFC3339Nano) }

// RandomID returns a crypto/rand hex identifier of n random bytes.
func RandomID(n int) string {
	b := make([]byte, n)
	if _, err := readRandom(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
