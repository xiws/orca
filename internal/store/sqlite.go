package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/xiws/orca/internal/domain"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Store owns the workspace lock until Close. All writes use short, immediate
// transactions; no external operation is performed while a transaction is open.
type Store struct {
	db     *sql.DB
	lock   *os.File
	mu     sync.Mutex
	closed bool
}

// Open opens workspace/.orca/state.sqlite3. Existing configuration directories and
// credential files are never chmod'ed or read.
func Open(workspace string) (_ *Store, err error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace is not a directory: %s", root)
	}
	dir := filepath.Join(root, ".orca")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err = os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("state directory is not a real directory: %s", dir)
	}
	lock, err := privateFile(filepath.Join(dir, "run.lock"))
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("workspace is already locked: %w", domain.ErrConflict)
		}
		return nil, err
	}
	s := &Store{lock: lock}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	path := filepath.Join(dir, "state.sqlite3")
	// Pre-create sidecars privately: this also protects an existing public .orca
	// directory without changing its permissions or the process-global umask.
	for _, name := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		f, e := privateFile(name)
		if e != nil {
			return nil, e
		}
		if e = f.Close(); e != nil {
			return nil, e
		}
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("_txlock", "immediate")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	s.db, err = sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	switch version {
	case 0:
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
			return nil, err
		}
		if count != 0 {
			return nil, fmt.Errorf("refusing an unversioned nonempty state database")
		}
		if _, err = tx.ExecContext(ctx, schema); err != nil {
			return nil, err
		}
	case 1:
		if _, err = tx.ExecContext(ctx, "ALTER TABLE events ADD COLUMN assistant_sequence INTEGER NOT NULL DEFAULT 0 CHECK(assistant_sequence >= 0); PRAGMA user_version = 2;"); err != nil {
			return nil, err
		}
	case schemaVersion:
	default:
		return nil, fmt.Errorf("unsupported state schema version %d (want %d)", version, schemaVersion)
	}
	if err = tx.Commit(); err != nil {
		return nil, storageError(err)
	}
	return s, nil
}

// O_NOFOLLOW and fstat prevent accidental chmod of symlinks, special files, or
// hard-linked credentials. Only files owned exclusively by this store qualify.
func privateFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, fmt.Errorf("open state file %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		f.Close()
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		f.Close()
		return nil, fmt.Errorf("state file must be a regular, unshared file: %s", path)
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	if s.lock != nil {
		err = errors.Join(err, unix.Flock(int(s.lock.Fd()), unix.LOCK_UN), s.lock.Close())
	}
	return err
}

func storageError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		switch coded.Code() {
		case sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY, sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_ROWID:
			return fmt.Errorf("%w: %v", domain.ErrConflict, err)
		}
	}
	return err
}

func optionalID[T ~int64](id T) any {
	if id == 0 {
		return nil
	}
	return int64(id)
}
