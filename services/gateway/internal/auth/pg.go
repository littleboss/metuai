package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(ctx context.Context, dsn string) (*PGStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	_, err = pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_idx ON users (lower(email));
`)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &PGStore{pool: pool}, nil
}

// NewPGStoreFromPool 复用已有连接池（与 meeting.PGStore 同库）。
func NewPGStoreFromPool(ctx context.Context, pool *pgxpool.Pool) (*PGStore, error) {
	_, err := pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_idx ON users (lower(email));
`)
	if err != nil {
		return nil, err
	}
	return &PGStore{pool: pool}, nil
}

func (s *PGStore) CreateUser(email, passwordHash, displayName string) (User, error) {
	key := NormalizeEmail(email)
	if key == "" {
		return User{}, errors.New("email required")
	}
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = localPart(key)
	}
	id := "usr_" + time.Now().UTC().Format("20060102150405")
	// 避免同秒冲突：追加短随机。
	id = id + "_" + itoa(int(time.Now().UnixNano()%100000))
	now := time.Now().UTC()
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO users (id, email, display_name, password_hash, created_at) VALUES ($1,$2,$3,$4,$5)`,
		id, key, name, passwordHash, now,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") ||
			strings.Contains(err.Error(), "duplicate") {
			return User{}, ErrDuplicateEmail
		}
		return User{}, err
	}
	return User{ID: id, Email: key, DisplayName: name, PasswordHash: passwordHash, CreatedAt: now}, nil
}

func (s *PGStore) FindByEmail(email string) (User, error) {
	key := NormalizeEmail(email)
	var u User
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, email, display_name, password_hash, created_at FROM users WHERE lower(email)=$1`,
		key,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return u, nil
}
