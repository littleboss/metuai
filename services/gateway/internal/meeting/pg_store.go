package meeting

import (
	"context"
	"fmt"
	"time"

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
CREATE TABLE IF NOT EXISTS meetings (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  organizer_id TEXT NOT NULL,
  locked BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS recording_acks (
  meeting_id TEXT NOT NULL,
  principal_key TEXT NOT NULL,
  acked_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (meeting_id, principal_key)
);
`)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &PGStore{pool: pool}, nil
}

func (s *PGStore) Create(title, organizerID, plainPassword string) (Meeting, string, error) {
	mem := NewMemoryStore()
	m, plain, err := mem.Create(title, organizerID, plainPassword)
	if err != nil {
		return Meeting{}, "", err
	}
	_, err = s.pool.Exec(context.Background(),
		`INSERT INTO meetings (id, title, password_hash, organizer_id, locked, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		m.ID, m.Title, m.PasswordHash, m.OrganizerID, m.Locked, m.CreatedAt,
	)
	if err != nil {
		return Meeting{}, "", err
	}
	return m, plain, nil
}

func (s *PGStore) Get(id string) (Meeting, bool) {
	var m Meeting
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, title, password_hash, organizer_id, locked, created_at FROM meetings WHERE id=$1`, id,
	).Scan(&m.ID, &m.Title, &m.PasswordHash, &m.OrganizerID, &m.Locked, &m.CreatedAt)
	if err != nil {
		return Meeting{}, false
	}
	return m, true
}

func (s *PGStore) CheckPassword(id, plain string) bool {
	m, ok := s.Get(id)
	if !ok {
		return false
	}
	return m.PasswordHash == HashPassword(plain)
}

func (s *PGStore) SetLocked(id string, locked bool) error {
	tag, err := s.pool.Exec(context.Background(), `UPDATE meetings SET locked=$2 WHERE id=$1`, id, locked)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("meeting not found")
	}
	return nil
}

func (s *PGStore) AckRecording(meetingID, principalKey string) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO recording_acks (meeting_id, principal_key, acked_at)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (meeting_id, principal_key) DO NOTHING`,
		meetingID, principalKey, time.Now().UTC(),
	)
	return err
}

func (s *PGStore) HasAck(meetingID, principalKey string) bool {
	var n int
	err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(1) FROM recording_acks WHERE meeting_id=$1 AND principal_key=$2`,
		meetingID, principalKey,
	).Scan(&n)
	return err == nil && n == 1
}

var _ Repository = (*PGStore)(nil)
