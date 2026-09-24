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

// Store 持有工作区文件锁直到 Close。所有写入使用短事务立即提交，
// 不会在事务打开时执行任何外部操作。
type Store struct {
	db     *sql.DB    // SQLite 数据库连接
	lock   *os.File   // 工作区排他锁文件
	mu     sync.Mutex // 序列化所有写操作
	closed bool       // 是否已关闭
}

// Open 打开 workspace/.orca/state.sqlite3 状态数据库。
// 不会对已有的配置目录和凭据文件进行 chmod 或读取。
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
	// 创建 .orca 状态目录（权限 0700）
	dir := filepath.Join(root, ".orca")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err = os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	// 确保状态目录是真实目录而非符号链接
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("state directory is not a real directory: %s", dir)
	}
	// 创建并获取排他锁文件
	lock, err := privateFile(filepath.Join(dir, "run.lock"))
	if err != nil {
		return nil, err
	}
	// 尝试获取非阻塞排他锁
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
	// 预先以私有权限创建数据库及其附属文件，保护已有公开 .orca 目录
	for _, name := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		f, e := privateFile(name)
		if e != nil {
			return nil, e
		}
		if e = f.Close(); e != nil {
			return nil, e
		}
	}
	// 配置 SQLite 连接参数：立即事务、外键、WAL 日志、全同步
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
	// 限制为单连接以确保 WAL 模式和锁的正确性
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
	// 根据 schema 版本号执行迁移或初始化
	switch version {
	case 0:
		// 空数据库：拒绝非空的无版本数据库
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
		// v1 -> v2 迁移：为 events 表添加 assistant_sequence 列
		if _, err = tx.ExecContext(ctx, "ALTER TABLE events ADD COLUMN assistant_sequence INTEGER NOT NULL DEFAULT 0 CHECK(assistant_sequence >= 0); PRAGMA user_version = 2;"); err != nil {
			return nil, err
		}
	case schemaVersion:
		// 已是最新版本，无需迁移
	default:
		return nil, fmt.Errorf("unsupported state schema version %d (want %d)", version, schemaVersion)
	}
	if err = tx.Commit(); err != nil {
		return nil, storageError(err)
	}
	return s, nil
}

// privateFile 以安全方式打开或创建文件：使用 O_NOFOLLOW 防止符号链接，
// fstat 验证文件为普通文件且硬链接数为 1，避免对凭据文件等的意外操作。
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
	// 验证文件为普通文件且无共享硬链接
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

// Close 关闭数据库连接并释放工作区锁
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

// storageError 将数据库错误转换为领域错误：
// 无行匹配转换为 ErrNotFound，唯一约束冲突转换为 ErrConflict
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

// optionalID 将零值 ID 转换为 nil，用于 SQL 参数中可选的外键引用
func optionalID[T ~int64](id T) any {
	if id == 0 {
		return nil
	}
	return int64(id)
}
