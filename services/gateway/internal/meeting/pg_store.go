package meeting

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	lktoken "metuai/services/gateway/internal/livekit"
)

type PGStore struct {
	pool *pgxpool.Pool
}

// Pool 供同库模块（如 auth.users）复用连接池。
func (s *PGStore) Pool() *pgxpool.Pool {
	return s.pool
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
  ended BOOLEAN NOT NULL DEFAULT FALSE,
  ended_at TIMESTAMPTZ,
  last_active_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  pipeline_stage TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL
);
ALTER TABLE meetings ADD COLUMN IF NOT EXISTS ended BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE meetings ADD COLUMN IF NOT EXISTS ended_at TIMESTAMPTZ;
ALTER TABLE meetings ADD COLUMN IF NOT EXISTS last_active_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE meetings ADD COLUMN IF NOT EXISTS pipeline_stage TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS recording_acks (
  meeting_id TEXT NOT NULL,
  principal_key TEXT NOT NULL,
  acked_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (meeting_id, principal_key)
);
CREATE TABLE IF NOT EXISTS meeting_members (
  meeting_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  role TEXT NOT NULL,
  display_name_snapshot TEXT NOT NULL DEFAULT '',
  invited_at TIMESTAMPTZ NOT NULL,
  joined_at TIMESTAMPTZ,
  PRIMARY KEY (meeting_id, user_id)
);
CREATE INDEX IF NOT EXISTS meeting_members_user_created_idx
  ON meeting_members (user_id, invited_at DESC);
INSERT INTO meeting_members (meeting_id, user_id, role, display_name_snapshot, invited_at)
SELECT id, organizer_id, 'organizer', '', created_at FROM meetings
ON CONFLICT (meeting_id, user_id) DO NOTHING;
CREATE TABLE IF NOT EXISTS meeting_kicks (
  meeting_id TEXT NOT NULL,
  identity TEXT NOT NULL,
  kicked_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (meeting_id, identity)
);
CREATE TABLE IF NOT EXISTS chat_messages (
  id TEXT PRIMARY KEY,
  meeting_id TEXT NOT NULL,
  sender_key TEXT NOT NULL,
  display_name TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS chat_messages_meeting_created_idx
  ON chat_messages (meeting_id, created_at);
CREATE TABLE IF NOT EXISTS audit_events (
  id TEXT PRIMARY KEY,
  meeting_id TEXT NOT NULL DEFAULT '',
  actor_key TEXT NOT NULL,
  action TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_events_meeting_created_idx
  ON audit_events (meeting_id, created_at);
CREATE TABLE IF NOT EXISTS media_artifacts (
  id TEXT PRIMARY KEY,
  meeting_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  status TEXT NOT NULL,
  object_key TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  egress_id TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL
);
ALTER TABLE media_artifacts ADD COLUMN IF NOT EXISTS egress_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS media_artifacts_meeting_idx
  ON media_artifacts (meeting_id, created_at);
CREATE TABLE IF NOT EXISTS transcript_segments (
  id TEXT PRIMARY KEY,
  meeting_id TEXT NOT NULL,
  track_id TEXT NOT NULL DEFAULT '',
  speaker_user_id TEXT NOT NULL DEFAULT '',
  speaker_display_name TEXT NOT NULL DEFAULT '',
  language TEXT NOT NULL DEFAULT 'zh-CN',
  start_ms INT NOT NULL DEFAULT 0,
  end_ms INT NOT NULL DEFAULT 0,
  text TEXT NOT NULL,
  asr_model TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'egress',
  confidence DOUBLE PRECISION
);
CREATE INDEX IF NOT EXISTS transcript_segments_meeting_idx
  ON transcript_segments (meeting_id, start_ms);
CREATE TABLE IF NOT EXISTS meeting_summaries (
  meeting_id TEXT PRIMARY KEY,
  summary TEXT NOT NULL,
  decisions_json TEXT NOT NULL DEFAULT '[]',
  action_items_json TEXT NOT NULL DEFAULT '[]',
  risks_json TEXT NOT NULL DEFAULT '[]',
  open_questions_json TEXT NOT NULL DEFAULT '[]',
  original_json TEXT NOT NULL DEFAULT '',
  revised_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL
);
ALTER TABLE meeting_summaries ADD COLUMN IF NOT EXISTS original_json TEXT NOT NULL DEFAULT '';
ALTER TABLE meeting_summaries ADD COLUMN IF NOT EXISTS revised_at TIMESTAMPTZ;
CREATE TABLE IF NOT EXISTS summary_revisions (
  id TEXT PRIMARY KEY,
  meeting_id TEXT NOT NULL,
  actor_key TEXT NOT NULL,
  patch_json TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS summary_revisions_meeting_idx
  ON summary_revisions (meeting_id, created_at);
ALTER TABLE media_artifacts ADD COLUMN IF NOT EXISTS participant_key TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS meeting_guest_emails (
  meeting_id TEXT NOT NULL,
  email TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT 'participant',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (meeting_id, email)
);
ALTER TABLE meeting_guest_emails ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'participant';
CREATE TABLE IF NOT EXISTS meeting_shares (
  meeting_id TEXT NOT NULL,
  email TEXT NOT NULL,
  created_by TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (meeting_id, email)
);
CREATE TABLE IF NOT EXISTS guest_email_challenges (
  meeting_id TEXT NOT NULL,
  guest_id TEXT NOT NULL,
  email TEXT NOT NULL,
  code_hash TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  verified_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (meeting_id, guest_id)
);
ALTER TABLE guest_email_challenges ADD COLUMN IF NOT EXISTS token_hash TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS guest_email_challenges_token_idx
  ON guest_email_challenges (meeting_id, token_hash);
CREATE TABLE IF NOT EXISTS pipeline_tasks (
  id TEXT PRIMARY KEY,
  meeting_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  status TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 3,
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_until TIMESTAMPTZ,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS pipeline_tasks_claim_idx
  ON pipeline_tasks (status, created_at);
CREATE TABLE IF NOT EXISTS retention_policy (
  id TEXT PRIMARY KEY,
  media_ttl_seconds BIGINT NOT NULL,
  video_ttl_seconds BIGINT NOT NULL,
  knowledge_ttl_seconds BIGINT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  updated_by TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS meeting_guest_presence (
  meeting_id TEXT NOT NULL,
  guest_id TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (meeting_id, guest_id)
);
`)
	if err != nil {
		pool.Close()
		return nil, err
	}
	_, _ = pool.Exec(ctx, breakGlassSchema)
	return &PGStore{pool: pool}, nil
}

// Ping 供 /readyz 复用已建立的连接池。
func (s *PGStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *PGStore) Create(title, organizerID, plainPassword string) (Meeting, string, error) {
	mem := NewMemoryStore()
	m, plain, err := mem.Create(title, organizerID, plainPassword)
	if err != nil {
		return Meeting{}, "", err
	}
	tx, err := s.pool.Begin(context.Background())
	if err != nil {
		return Meeting{}, "", err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(context.Background(),
		`INSERT INTO meetings (id, title, password_hash, organizer_id, locked, ended, ended_at, last_active_at, pipeline_stage, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		m.ID, m.Title, m.PasswordHash, m.OrganizerID, m.Locked, m.Ended, m.EndedAt, m.LastActiveAt, m.PipelineStage, m.CreatedAt,
	); err != nil {
		return Meeting{}, "", err
	}
	if _, err = tx.Exec(context.Background(),
		`INSERT INTO meeting_members (meeting_id, user_id, role, display_name_snapshot, invited_at)
		 VALUES ($1,$2,$3,$4,$5)`,
		m.ID, organizerID, MeetingMemberOrganizer, "", m.CreatedAt,
	); err != nil {
		return Meeting{}, "", err
	}
	if err = tx.Commit(context.Background()); err != nil {
		return Meeting{}, "", err
	}
	return m, plain, nil
}

func (s *PGStore) Get(id string) (Meeting, bool) {
	var m Meeting
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, title, password_hash, organizer_id, locked, ended, ended_at, last_active_at, pipeline_stage, created_at
		 FROM meetings WHERE id=$1`, id,
	).Scan(&m.ID, &m.Title, &m.PasswordHash, &m.OrganizerID, &m.Locked, &m.Ended, &m.EndedAt, &m.LastActiveAt, &m.PipelineStage, &m.CreatedAt)
	if err != nil {
		return Meeting{}, false
	}
	return m, true
}

func (s *PGStore) AddMembers(meetingID string, members []MeetingMember) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	tx, err := s.pool.Begin(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	now := time.Now().UTC()
	for _, member := range members {
		member.UserID = strings.TrimSpace(member.UserID)
		if member.UserID == "" {
			continue
		}
		if memberRolePriority(member.Role) < 0 {
			return fmt.Errorf("invalid meeting member role")
		}
		if member.InvitedAt.IsZero() {
			member.InvitedAt = now
		}
		if _, err := tx.Exec(context.Background(),
			`INSERT INTO meeting_members (meeting_id, user_id, role, display_name_snapshot, invited_at, joined_at)
			 VALUES ($1,$2,$3,$4,$5,$6)
			 ON CONFLICT (meeting_id, user_id) DO UPDATE SET
			 role=CASE
			   WHEN CASE meeting_members.role WHEN 'organizer' THEN 3 WHEN 'co_organizer' THEN 2 WHEN 'invited' THEN 1 ELSE 0 END
			      >= CASE EXCLUDED.role WHEN 'organizer' THEN 3 WHEN 'co_organizer' THEN 2 WHEN 'invited' THEN 1 ELSE 0 END
			   THEN meeting_members.role ELSE EXCLUDED.role END,
			 display_name_snapshot=CASE
			   WHEN EXCLUDED.display_name_snapshot <> '' THEN EXCLUDED.display_name_snapshot
			   ELSE meeting_members.display_name_snapshot END`,
			meetingID, member.UserID, member.Role, strings.TrimSpace(member.DisplayNameSnapshot), member.InvitedAt, member.JoinedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit(context.Background())
}

func (s *PGStore) MarkMemberJoined(meetingID, userID, displayName string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("employee user id required")
	}
	now := time.Now().UTC()
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO meeting_members (meeting_id, user_id, role, display_name_snapshot, invited_at, joined_at)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (meeting_id, user_id) DO UPDATE SET
		 display_name_snapshot=CASE
		   WHEN meeting_members.display_name_snapshot = '' THEN EXCLUDED.display_name_snapshot
		   ELSE meeting_members.display_name_snapshot END,
		 joined_at=COALESCE(meeting_members.joined_at, EXCLUDED.joined_at)`,
		meetingID, userID, MeetingMemberParticipant, strings.TrimSpace(displayName), now, now,
	)
	return err
}

func (s *PGStore) IsInvitedEmployee(meetingID, userID string) bool {
	var exists bool
	err := s.pool.QueryRow(context.Background(),
		`SELECT EXISTS(
			SELECT 1 FROM meeting_members
			WHERE meeting_id=$1 AND user_id=$2 AND role IN ('organizer', 'co_organizer', 'invited')
		)`, meetingID, userID,
	).Scan(&exists)
	return err == nil && exists
}

func (s *PGStore) IsOrganizerOrCoOrganizer(meetingID, userID string) bool {
	var exists bool
	err := s.pool.QueryRow(context.Background(),
		`SELECT EXISTS(
			SELECT 1 FROM meeting_members
			WHERE meeting_id=$1 AND user_id=$2 AND role IN ('organizer', 'co_organizer')
		)`, meetingID, userID,
	).Scan(&exists)
	return err == nil && exists
}

func (s *PGStore) ListMeetingsForEmployee(userID string) ([]Meeting, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT m.id, m.title, m.password_hash, m.organizer_id, m.locked, m.ended, m.ended_at,
			m.last_active_at, m.pipeline_stage, m.created_at
		 FROM meetings m
		 JOIN meeting_members member ON member.meeting_id=m.id
		 WHERE member.user_id=$1
		 ORDER BY m.created_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Meeting, 0)
	for rows.Next() {
		var meeting Meeting
		if err := rows.Scan(
			&meeting.ID, &meeting.Title, &meeting.PasswordHash, &meeting.OrganizerID, &meeting.Locked,
			&meeting.Ended, &meeting.EndedAt, &meeting.LastActiveAt, &meeting.PipelineStage, &meeting.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, meeting)
	}
	return out, rows.Err()
}

func (s *PGStore) ListActive() ([]Meeting, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, title, password_hash, organizer_id, locked, ended, ended_at, last_active_at, pipeline_stage, created_at
		 FROM meetings WHERE ended=FALSE`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Meeting, 0)
	for rows.Next() {
		var m Meeting
		if err := rows.Scan(&m.ID, &m.Title, &m.PasswordHash, &m.OrganizerID, &m.Locked, &m.Ended, &m.EndedAt, &m.LastActiveAt, &m.PipelineStage, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *PGStore) ListEnded() ([]Meeting, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, title, password_hash, organizer_id, locked, ended, ended_at, last_active_at, pipeline_stage, created_at
		 FROM meetings WHERE ended=TRUE`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Meeting, 0)
	for rows.Next() {
		var m Meeting
		if err := rows.Scan(&m.ID, &m.Title, &m.PasswordHash, &m.OrganizerID, &m.Locked, &m.Ended, &m.EndedAt, &m.LastActiveAt, &m.PipelineStage, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *PGStore) ListEndedNeedingPipeline() ([]Meeting, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, title, password_hash, organizer_id, locked, ended, ended_at, last_active_at, pipeline_stage, created_at
		 FROM meetings
		 WHERE ended=TRUE AND pipeline_stage <> $1 AND pipeline_stage <> $2`,
		StageReady, StageManualReview,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Meeting, 0)
	for rows.Next() {
		var m Meeting
		if err := rows.Scan(&m.ID, &m.Title, &m.PasswordHash, &m.OrganizerID, &m.Locked, &m.Ended, &m.EndedAt, &m.LastActiveAt, &m.PipelineStage, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *PGStore) CheckPassword(id, plain string) bool {
	m, ok := s.Get(id)
	if !ok {
		return false
	}
	return passwordMatches(m.PasswordHash, plain)
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

func (s *PGStore) ResetPassword(id string) (string, error) {
	plain, err := randomPassword()
	if err != nil {
		return "", err
	}
	passwordHash, err := hashPassword(plain)
	if err != nil {
		return "", err
	}
	tag, err := s.pool.Exec(context.Background(),
		`UPDATE meetings SET password_hash=$2 WHERE id=$1`, id, passwordHash,
	)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", fmt.Errorf("meeting not found")
	}
	return plain, nil
}

func (s *PGStore) End(id string) error {
	now := time.Now().UTC()
	tag, err := s.pool.Exec(context.Background(),
		`UPDATE meetings SET ended=TRUE, ended_at=$2,
		 pipeline_stage=CASE WHEN pipeline_stage='' OR pipeline_stage IS NULL THEN $3 ELSE pipeline_stage END
		 WHERE id=$1`, id, now, StageRecordingFinalized,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("meeting not found")
	}
	return nil
}

func (s *PGStore) TouchActivity(id string) error {
	tag, err := s.pool.Exec(context.Background(),
		`UPDATE meetings SET last_active_at=$2 WHERE id=$1 AND ended=FALSE`, id, time.Now().UTC(),
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("meeting not found")
	}
	return nil
}

func (s *PGStore) SetPipelineStage(id, stage string) error {
	tag, err := s.pool.Exec(context.Background(),
		`UPDATE meetings SET pipeline_stage=$2 WHERE id=$1`, id, stage,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("meeting not found")
	}
	return nil
}

func (s *PGStore) AckRecording(meetingID, principalKey string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
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

func (s *PGStore) Kick(meetingID, identity string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	identity = lktoken.UserKey(identity)
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO meeting_kicks (meeting_id, identity, kicked_at)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (meeting_id, identity) DO NOTHING`,
		meetingID, identity, time.Now().UTC(),
	)
	return err
}

func (s *PGStore) IsKicked(meetingID, identity string) bool {
	identity = lktoken.UserKey(identity)
	var n int
	err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(1) FROM meeting_kicks WHERE meeting_id=$1 AND identity=$2`,
		meetingID, identity,
	).Scan(&n)
	return err == nil && n == 1
}

func (s *PGStore) AddChat(msg ChatMessage) (ChatMessage, error) {
	if _, ok := s.Get(msg.MeetingID); !ok {
		return ChatMessage{}, fmt.Errorf("meeting not found")
	}
	if msg.ID == "" {
		id, err := RandomID("msg_")
		if err != nil {
			return ChatMessage{}, err
		}
		msg.ID = id
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO chat_messages (id, meeting_id, sender_key, display_name, body, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		msg.ID, msg.MeetingID, msg.SenderKey, msg.DisplayName, msg.Body, msg.CreatedAt,
	)
	if err != nil {
		return ChatMessage{}, err
	}
	return msg, nil
}

func (s *PGStore) ListChat(meetingID string) ([]ChatMessage, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, meeting_id, sender_key, display_name, body, created_at
		 FROM chat_messages WHERE meeting_id=$1 ORDER BY created_at ASC`, meetingID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ChatMessage, 0)
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.ID, &m.MeetingID, &m.SenderKey, &m.DisplayName, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *PGStore) AppendAudit(event AuditEvent) error {
	if event.ID == "" {
		id, err := RandomID("aud_")
		if err != nil {
			return err
		}
		event.ID = id
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO audit_events (id, meeting_id, actor_key, action, detail, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		event.ID, event.MeetingID, event.ActorKey, event.Action, event.Detail, event.CreatedAt,
	)
	return err
}

func (s *PGStore) ListAudit(meetingID string) ([]AuditEvent, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, meeting_id, actor_key, action, detail, created_at
		 FROM audit_events WHERE meeting_id=$1 ORDER BY created_at ASC`, meetingID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AuditEvent, 0)
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.MeetingID, &e.ActorKey, &e.Action, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PGStore) AddMediaArtifact(a MediaArtifact) (MediaArtifact, error) {
	if _, ok := s.Get(a.MeetingID); !ok {
		return MediaArtifact{}, fmt.Errorf("meeting not found")
	}
	if a.ID == "" {
		id, err := RandomID("med_")
		if err != nil {
			return MediaArtifact{}, err
		}
		a.ID = id
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if a.Status == "" {
		a.Status = "pending"
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO media_artifacts (id, meeting_id, kind, status, object_key, detail, egress_id, participant_key, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		a.ID, a.MeetingID, a.Kind, a.Status, a.ObjectKey, a.Detail, a.EgressID, a.ParticipantKey, a.CreatedAt,
	)
	if err != nil {
		return MediaArtifact{}, err
	}
	return a, nil
}

func (s *PGStore) ListMediaArtifacts(meetingID string) ([]MediaArtifact, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, meeting_id, kind, status, object_key, detail, egress_id, COALESCE(participant_key, ''), created_at
		 FROM media_artifacts WHERE meeting_id=$1 ORDER BY created_at ASC`, meetingID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MediaArtifact, 0)
	for rows.Next() {
		var a MediaArtifact
		if err := rows.Scan(&a.ID, &a.MeetingID, &a.Kind, &a.Status, &a.ObjectKey, &a.Detail, &a.EgressID, &a.ParticipantKey, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PGStore) UpdateMediaArtifactStatus(id, status, detail string) error {
	tag, err := s.pool.Exec(context.Background(),
		`UPDATE media_artifacts SET status=$2, detail=$3 WHERE id=$1`, id, status, detail,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("media artifact not found")
	}
	return nil
}

func (s *PGStore) ReplaceTranscript(meetingID string, segments []TranscriptSegment) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	tx, err := s.pool.Begin(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `DELETE FROM transcript_segments WHERE meeting_id=$1`, meetingID); err != nil {
		return err
	}
	for _, seg := range segments {
		if seg.ID == "" {
			id, err := RandomID("seg_")
			if err != nil {
				return err
			}
			seg.ID = id
		}
		if _, err := tx.Exec(context.Background(),
			`INSERT INTO transcript_segments
			 (id, meeting_id, track_id, speaker_user_id, speaker_display_name, language, start_ms, end_ms, text, asr_model, source, confidence)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			seg.ID, meetingID, seg.TrackID, seg.SpeakerUserID, seg.SpeakerDisplayName, seg.Language,
			seg.StartMs, seg.EndMs, seg.Text, seg.ASRModel, seg.Source, seg.Confidence,
		); err != nil {
			return err
		}
	}
	return tx.Commit(context.Background())
}

func (s *PGStore) ListTranscript(meetingID string) ([]TranscriptSegment, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, meeting_id, track_id, speaker_user_id, speaker_display_name, language, start_ms, end_ms, text, asr_model, source, confidence
		 FROM transcript_segments WHERE meeting_id=$1 ORDER BY start_ms ASC`, meetingID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TranscriptSegment, 0)
	for rows.Next() {
		var seg TranscriptSegment
		if err := rows.Scan(
			&seg.ID, &seg.MeetingID, &seg.TrackID, &seg.SpeakerUserID, &seg.SpeakerDisplayName,
			&seg.Language, &seg.StartMs, &seg.EndMs, &seg.Text, &seg.ASRModel, &seg.Source, &seg.Confidence,
		); err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	return out, rows.Err()
}

func (s *PGStore) UpsertSummary(summary MeetingSummary) error {
	if _, ok := s.Get(summary.MeetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	if existing, ok := s.GetSummary(summary.MeetingID); ok {
		if summary.OriginalJSON == "" {
			summary.OriginalJSON = existing.OriginalJSON
		}
		if !existing.CreatedAt.IsZero() {
			summary.CreatedAt = existing.CreatedAt
		}
	} else if summary.OriginalJSON == "" {
		summary.OriginalJSON = captureOriginalJSON(summary)
	}
	if summary.CreatedAt.IsZero() {
		summary.CreatedAt = time.Now().UTC()
	}
	dec, _ := json.Marshal(summary.Decisions)
	act, _ := json.Marshal(summary.ActionItems)
	risks, _ := json.Marshal(summary.Risks)
	openQ, _ := json.Marshal(summary.OpenQuestions)
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO meeting_summaries
		 (meeting_id, summary, decisions_json, action_items_json, risks_json, open_questions_json, original_json, revised_at, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (meeting_id) DO UPDATE SET
		   summary=EXCLUDED.summary,
		   decisions_json=EXCLUDED.decisions_json,
		   action_items_json=EXCLUDED.action_items_json,
		   risks_json=EXCLUDED.risks_json,
		   open_questions_json=EXCLUDED.open_questions_json,
		   original_json=CASE WHEN meeting_summaries.original_json = '' THEN EXCLUDED.original_json ELSE meeting_summaries.original_json END,
		   revised_at=EXCLUDED.revised_at,
		   created_at=meeting_summaries.created_at`,
		summary.MeetingID, summary.Summary, string(dec), string(act), string(risks), string(openQ), summary.OriginalJSON, summary.RevisedAt, summary.CreatedAt,
	)
	return err
}

func (s *PGStore) GetSummary(meetingID string) (MeetingSummary, bool) {
	var sum MeetingSummary
	var dec, act, risks, openQ string
	err := s.pool.QueryRow(context.Background(),
		`SELECT meeting_id, summary, decisions_json, action_items_json, risks_json, open_questions_json,
		        COALESCE(original_json, ''), revised_at, created_at
		 FROM meeting_summaries WHERE meeting_id=$1`, meetingID,
	).Scan(&sum.MeetingID, &sum.Summary, &dec, &act, &risks, &openQ, &sum.OriginalJSON, &sum.RevisedAt, &sum.CreatedAt)
	if err != nil {
		return MeetingSummary{}, false
	}
	_ = json.Unmarshal([]byte(dec), &sum.Decisions)
	_ = json.Unmarshal([]byte(act), &sum.ActionItems)
	_ = json.Unmarshal([]byte(risks), &sum.Risks)
	_ = json.Unmarshal([]byte(openQ), &sum.OpenQuestions)
	return sum, true
}

func (s *PGStore) AddGuestEmail(meetingID, email string) error {
	return s.AddGuestEmailSource(meetingID, email, guestEmailSourceParticipant)
}

func (s *PGStore) AddGuestEmailSource(meetingID, email, source string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	email = normalizeGuestEmail(email)
	if email == "" {
		return nil
	}
	if source != guestEmailSourceShared {
		source = guestEmailSourceParticipant
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO meeting_guest_emails (meeting_id, email, source, created_at)
		 VALUES ($1,$2,$3,NOW())
		 ON CONFLICT (meeting_id, email) DO UPDATE SET
		 source = CASE
		   WHEN meeting_guest_emails.source = 'participant' OR EXCLUDED.source = 'participant'
		   THEN 'participant'
		   ELSE meeting_guest_emails.source
		 END`,
		meetingID, email, source)
	return err
}

func (s *PGStore) ListGuestEmails(meetingID string) ([]string, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT email FROM meeting_guest_emails WHERE meeting_id=$1 ORDER BY email`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *PGStore) AddShare(meetingID, email, createdBy string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	email = normalizeGuestEmail(email)
	if email == "" {
		return fmt.Errorf("invalid_email")
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO meeting_shares (meeting_id, email, created_by, created_at)
		 VALUES ($1,$2,$3,NOW()) ON CONFLICT (meeting_id, email) DO NOTHING`,
		meetingID, email, strings.TrimSpace(createdBy))
	return err
}

func (s *PGStore) ListShares(meetingID string) ([]MeetingShare, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT s.email, s.created_by, s.created_at,
		        EXISTS (
		          SELECT 1 FROM meeting_guest_emails g
		          WHERE g.meeting_id = s.meeting_id AND g.email = s.email
		        ) AS verified
		 FROM meeting_shares s
		 WHERE s.meeting_id=$1
		 ORDER BY s.email`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MeetingShare, 0)
	for rows.Next() {
		var share MeetingShare
		if err := rows.Scan(&share.Email, &share.CreatedBy, &share.CreatedAt, &share.Verified); err != nil {
			return nil, err
		}
		out = append(out, share)
	}
	return out, nil
}

func (s *PGStore) RemoveShare(meetingID, email string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	email = normalizeGuestEmail(email)
	_, err := s.pool.Exec(context.Background(),
		`DELETE FROM meeting_shares WHERE meeting_id=$1 AND email=$2`, meetingID, email)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(context.Background(),
		`DELETE FROM meeting_guest_emails
		 WHERE meeting_id=$1 AND email=$2 AND source=$3`,
		meetingID, email, guestEmailSourceShared)
	return err
}

func (s *PGStore) HasShare(meetingID, email string) bool {
	email = normalizeGuestEmail(email)
	var n int
	err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(1) FROM meeting_shares WHERE meeting_id=$1 AND email=$2`,
		meetingID, email,
	).Scan(&n)
	return err == nil && n == 1
}

func (s *PGStore) SaveGuestEmailChallenge(challenge GuestEmailChallenge) error {
	if _, ok := s.Get(challenge.MeetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	challenge.GuestID = strings.TrimSpace(challenge.GuestID)
	challenge.Email = normalizeGuestEmail(challenge.Email)
	if challenge.GuestID == "" || challenge.Email == "" || challenge.CodeHash == "" || challenge.ExpiresAt.IsZero() {
		return fmt.Errorf("invalid guest email challenge")
	}
	if challenge.CreatedAt.IsZero() {
		challenge.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO guest_email_challenges
		 (meeting_id, guest_id, email, code_hash, token_hash, expires_at, attempts, verified_at, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,0,NULL,$7)
		 ON CONFLICT (meeting_id, guest_id) DO UPDATE SET
		 email=EXCLUDED.email,
		 code_hash=EXCLUDED.code_hash,
		 token_hash=EXCLUDED.token_hash,
		 expires_at=EXCLUDED.expires_at,
		 attempts=0,
		 verified_at=NULL,
		 created_at=EXCLUDED.created_at`,
		challenge.MeetingID, challenge.GuestID, challenge.Email, challenge.CodeHash, challenge.TokenHash, challenge.ExpiresAt, challenge.CreatedAt,
	)
	return err
}

func (s *PGStore) VerifyGuestEmailChallenge(meetingID, guestID, email, code string) (GuestEmailChallenge, error) {
	email = normalizeGuestEmail(email)
	tx, err := s.pool.Begin(context.Background())
	if err != nil {
		return GuestEmailChallenge{}, err
	}
	defer tx.Rollback(context.Background())
	var challenge GuestEmailChallenge
	err = tx.QueryRow(context.Background(),
		`SELECT meeting_id, guest_id, email, code_hash, expires_at, attempts, verified_at, created_at
		 FROM guest_email_challenges WHERE meeting_id=$1 AND guest_id=$2 FOR UPDATE`, meetingID, strings.TrimSpace(guestID),
	).Scan(
		&challenge.MeetingID, &challenge.GuestID, &challenge.Email, &challenge.CodeHash, &challenge.ExpiresAt,
		&challenge.Attempts, &challenge.VerifiedAt, &challenge.CreatedAt,
	)
	if err != nil {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationInvalid
	}
	if challenge.Email != email {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationInvalid
	}
	if challenge.VerifiedAt != nil {
		if err := tx.Commit(context.Background()); err != nil {
			return GuestEmailChallenge{}, err
		}
		return challenge, nil
	}
	if time.Now().UTC().After(challenge.ExpiresAt) {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationExpired
	}
	if challenge.Attempts >= guestEmailVerificationMaxAttempts {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationAttemptsExceeded
	}
	if bcrypt.CompareHashAndPassword([]byte(challenge.CodeHash), []byte(strings.TrimSpace(code))) != nil {
		if _, err := tx.Exec(context.Background(),
			`UPDATE guest_email_challenges SET attempts=attempts+1 WHERE meeting_id=$1 AND guest_id=$2`, meetingID, challenge.GuestID,
		); err != nil {
			return GuestEmailChallenge{}, err
		}
		if err := tx.Commit(context.Background()); err != nil {
			return GuestEmailChallenge{}, err
		}
		return GuestEmailChallenge{}, ErrGuestEmailVerificationInvalid
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(context.Background(),
		`UPDATE guest_email_challenges SET verified_at=$3 WHERE meeting_id=$1 AND guest_id=$2`, meetingID, challenge.GuestID, now,
	); err != nil {
		return GuestEmailChallenge{}, err
	}
	if err := tx.Commit(context.Background()); err != nil {
		return GuestEmailChallenge{}, err
	}
	challenge.VerifiedAt = &now
	return challenge, nil
}

func (s *PGStore) VerifyGuestMagicToken(meetingID, token string) (GuestEmailChallenge, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationInvalid
	}
	want := hashMagicToken(token)
	tx, err := s.pool.Begin(context.Background())
	if err != nil {
		return GuestEmailChallenge{}, err
	}
	defer tx.Rollback(context.Background())
	var challenge GuestEmailChallenge
	err = tx.QueryRow(context.Background(),
		`SELECT meeting_id, guest_id, email, code_hash, token_hash, expires_at, attempts, verified_at, created_at
		 FROM guest_email_challenges WHERE meeting_id=$1 AND token_hash=$2 AND token_hash <> '' FOR UPDATE`,
		meetingID, want,
	).Scan(
		&challenge.MeetingID, &challenge.GuestID, &challenge.Email, &challenge.CodeHash, &challenge.TokenHash,
		&challenge.ExpiresAt, &challenge.Attempts, &challenge.VerifiedAt, &challenge.CreatedAt,
	)
	if err != nil {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationInvalid
	}
	if challenge.VerifiedAt != nil {
		if err := tx.Commit(context.Background()); err != nil {
			return GuestEmailChallenge{}, err
		}
		return challenge, nil
	}
	if time.Now().UTC().After(challenge.ExpiresAt) {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationExpired
	}
	if challenge.Attempts >= guestEmailVerificationMaxAttempts {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationAttemptsExceeded
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(context.Background(),
		`UPDATE guest_email_challenges SET verified_at=$3 WHERE meeting_id=$1 AND guest_id=$2`,
		meetingID, challenge.GuestID, now,
	); err != nil {
		return GuestEmailChallenge{}, err
	}
	if err := tx.Commit(context.Background()); err != nil {
		return GuestEmailChallenge{}, err
	}
	challenge.VerifiedAt = &now
	return challenge, nil
}

func (s *PGStore) ListMeetingIDsForGuestEmail(email string) ([]string, error) {
	email = normalizeGuestEmail(email)
	rows, err := s.pool.Query(context.Background(),
		`SELECT meeting_id FROM meeting_guest_emails WHERE email=$1 ORDER BY meeting_id`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *PGStore) ListGuestIdentitiesForEmail(email string) ([]GuestIdentityRef, error) {
	email = normalizeGuestEmail(email)
	rows, err := s.pool.Query(context.Background(),
		`SELECT meeting_id, guest_id FROM guest_email_challenges WHERE email=$1 ORDER BY meeting_id, guest_id`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]GuestIdentityRef, 0)
	for rows.Next() {
		var ref GuestIdentityRef
		if err := rows.Scan(&ref.MeetingID, &ref.GuestID); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

func (s *PGStore) RewriteIdentityKeys(meetingID string, fromKeys []string, toKey string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	toKey = strings.TrimSpace(toKey)
	if toKey == "" {
		return nil
	}
	ctx := context.Background()
	for _, from := range fromKeys {
		from = strings.TrimSpace(from)
		if from == "" || from == toKey {
			continue
		}
		if _, err := s.pool.Exec(ctx,
			`UPDATE transcript_segments SET speaker_user_id=$3 WHERE meeting_id=$1 AND speaker_user_id=$2`,
			meetingID, from, toKey,
		); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx,
			`UPDATE media_artifacts SET participant_key=$3 WHERE meeting_id=$1 AND participant_key=$2`,
			meetingID, from, toKey,
		); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx,
			`UPDATE chat_messages SET sender_key=$3 WHERE meeting_id=$1 AND sender_key=$2`,
			meetingID, from, toKey,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *PGStore) SavePipelineTask(task PipelineTask) (PipelineTask, error) {
	if _, ok := s.Get(task.MeetingID); !ok {
		return PipelineTask{}, fmt.Errorf("meeting not found")
	}
	if task.ID == "" {
		id, err := RandomID("ptk_")
		if err != nil {
			return PipelineTask{}, err
		}
		task.ID = id
	}
	now := time.Now().UTC()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	task.UpdatedAt = now
	if task.MaxAttempts <= 0 {
		task.MaxAttempts = pipelineTaskMaxAttempts
	}
	if task.Status == "" {
		task.Status = PipelineTaskQueued
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO pipeline_tasks
		 (id, meeting_id, kind, status, attempts, max_attempts, lease_owner, lease_until, last_error, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		task.ID, task.MeetingID, task.Kind, task.Status, task.Attempts, task.MaxAttempts,
		task.LeaseOwner, task.LeaseUntil, task.LastError, task.CreatedAt, task.UpdatedAt,
	)
	if err != nil {
		return PipelineTask{}, err
	}
	return task, nil
}

func (s *PGStore) ClaimPipelineTasks(owner, kind string, limit int) ([]PipelineTask, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, fmt.Errorf("lease owner required")
	}
	if limit <= 0 {
		limit = 1
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(pipelineTaskLease)
	tx, err := s.pool.Begin(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	rows, err := tx.Query(context.Background(),
		`SELECT id, meeting_id, kind, status, attempts, max_attempts, lease_owner, lease_until, last_error, created_at, updated_at
		 FROM pipeline_tasks
		 WHERE (status = $1 OR status = $2 OR (status = $3 AND lease_until IS NOT NULL AND lease_until < $4))
		   AND attempts < max_attempts
		   AND ($5 = '' OR kind = $5)
		 ORDER BY created_at ASC
		 FOR UPDATE SKIP LOCKED
		 LIMIT $6`,
		PipelineTaskQueued, PipelineTaskFailed, PipelineTaskLeased, now, strings.TrimSpace(kind), limit,
	)
	if err != nil {
		return nil, err
	}
	tasks := make([]PipelineTask, 0, limit)
	for rows.Next() {
		var task PipelineTask
		if err := rows.Scan(
			&task.ID, &task.MeetingID, &task.Kind, &task.Status, &task.Attempts, &task.MaxAttempts,
			&task.LeaseOwner, &task.LeaseUntil, &task.LastError, &task.CreatedAt, &task.UpdatedAt,
		); err != nil {
			rows.Close()
			return nil, err
		}
		tasks = append(tasks, task)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]PipelineTask, 0, len(tasks))
	for _, task := range tasks {
		task.Status = PipelineTaskLeased
		task.LeaseOwner = owner
		task.LeaseUntil = &leaseUntil
		task.UpdatedAt = now
		if _, err := tx.Exec(context.Background(),
			`UPDATE pipeline_tasks SET status=$2, lease_owner=$3, lease_until=$4, updated_at=$5 WHERE id=$1`,
			task.ID, task.Status, task.LeaseOwner, task.LeaseUntil, task.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	if err := tx.Commit(context.Background()); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PGStore) GetPipelineTask(id string) (PipelineTask, bool) {
	var task PipelineTask
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, meeting_id, kind, status, attempts, max_attempts, lease_owner, lease_until, last_error, created_at, updated_at
		 FROM pipeline_tasks WHERE id=$1`, id,
	).Scan(
		&task.ID, &task.MeetingID, &task.Kind, &task.Status, &task.Attempts, &task.MaxAttempts,
		&task.LeaseOwner, &task.LeaseUntil, &task.LastError, &task.CreatedAt, &task.UpdatedAt,
	)
	if err != nil {
		return PipelineTask{}, false
	}
	return task, true
}

func (s *PGStore) UpdatePipelineTask(task PipelineTask) error {
	task.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(context.Background(),
		`UPDATE pipeline_tasks SET status=$2, attempts=$3, max_attempts=$4, lease_owner=$5, lease_until=$6, last_error=$7, updated_at=$8
		 WHERE id=$1`,
		task.ID, task.Status, task.Attempts, task.MaxAttempts, task.LeaseOwner, task.LeaseUntil, task.LastError, task.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("pipeline task not found")
	}
	return nil
}

func (s *PGStore) ListPipelineTasks(meetingID string) ([]PipelineTask, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, meeting_id, kind, status, attempts, max_attempts, lease_owner, lease_until, last_error, created_at, updated_at
		 FROM pipeline_tasks
		 WHERE ($1 = '' OR meeting_id = $1)
		 ORDER BY created_at ASC`, meetingID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PipelineTask, 0)
	for rows.Next() {
		var task PipelineTask
		if err := rows.Scan(
			&task.ID, &task.MeetingID, &task.Kind, &task.Status, &task.Attempts, &task.MaxAttempts,
			&task.LeaseOwner, &task.LeaseUntil, &task.LastError, &task.CreatedAt, &task.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

func (s *PGStore) ListEmployeeParticipantIDs(meetingID string) ([]string, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT principal_key FROM recording_acks WHERE meeting_id=$1 AND principal_key LIKE 'employee:%'`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	seen := map[string]struct{}{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		uid := strings.TrimPrefix(key, "employee:")
		if uid == "" {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		out = append(out, uid)
	}
	return out, nil
}

func (s *PGStore) ListGuestParticipantIDs(meetingID string) ([]string, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT principal_key FROM recording_acks WHERE meeting_id=$1 AND principal_key LIKE 'guest:%'`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	seen := map[string]struct{}{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		gid := strings.TrimPrefix(key, "guest:")
		if gid == "" {
			continue
		}
		if _, ok := seen[gid]; ok {
			continue
		}
		seen[gid] = struct{}{}
		out = append(out, gid)
	}
	slices.Sort(out)
	return out, rows.Err()
}

func (s *PGStore) UpsertGuestPresence(meetingID, guestID, displayName string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	guestID = strings.TrimSpace(strings.TrimPrefix(guestID, "guest:"))
	if guestID == "" {
		return fmt.Errorf("guest_id_required")
	}
	name := strings.TrimSpace(displayName)
	if name == "" {
		return nil
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO meeting_guest_presence (meeting_id, guest_id, display_name, updated_at)
		 VALUES ($1, $2, $3, NOW())
		 ON CONFLICT (meeting_id, guest_id) DO UPDATE SET display_name = EXCLUDED.display_name, updated_at = NOW()`,
		meetingID, guestID, name)
	return err
}

func (s *PGStore) ListGuestParticipants(meetingID string) ([]GuestParticipant, error) {
	ids, err := s.ListGuestParticipantIDs(meetingID)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT guest_id, display_name FROM meeting_guest_presence WHERE meeting_id=$1`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]GuestParticipant, 0, len(ids))
	for _, id := range ids {
		out = append(out, GuestParticipant{GuestID: id, DisplayName: names[id]})
	}
	return out, nil
}

func (s *PGStore) ListMembers(meetingID string) ([]MeetingMember, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT meeting_id, user_id, role, display_name_snapshot, invited_at, joined_at
		 FROM meeting_members WHERE meeting_id=$1 ORDER BY user_id`, meetingID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MeetingMember, 0)
	for rows.Next() {
		var member MeetingMember
		if err := rows.Scan(&member.MeetingID, &member.UserID, &member.Role, &member.DisplayNameSnapshot, &member.InvitedAt, &member.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, member)
	}
	return out, rows.Err()
}

func (s *PGStore) AppendSummaryRevision(rev SummaryRevision) (SummaryRevision, error) {
	if _, ok := s.Get(rev.MeetingID); !ok {
		return SummaryRevision{}, fmt.Errorf("meeting not found")
	}
	if rev.ID == "" {
		id, err := RandomID("rev_")
		if err != nil {
			return SummaryRevision{}, err
		}
		rev.ID = id
	}
	if rev.CreatedAt.IsZero() {
		rev.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO summary_revisions (id, meeting_id, actor_key, patch_json, created_at)
		 VALUES ($1,$2,$3,$4,$5)`,
		rev.ID, rev.MeetingID, rev.ActorKey, rev.PatchJSON, rev.CreatedAt,
	)
	if err != nil {
		return SummaryRevision{}, err
	}
	return rev, nil
}

func (s *PGStore) ListSummaryRevisions(meetingID string) ([]SummaryRevision, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, meeting_id, actor_key, patch_json, created_at
		 FROM summary_revisions WHERE meeting_id=$1 ORDER BY created_at ASC`, meetingID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SummaryRevision, 0)
	for rows.Next() {
		var rev SummaryRevision
		if err := rows.Scan(&rev.ID, &rev.MeetingID, &rev.ActorKey, &rev.PatchJSON, &rev.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rev)
	}
	return out, rows.Err()
}

func (s *PGStore) GetRetentionPolicy() (RetentionPolicy, error) {
	var p RetentionPolicy
	err := s.pool.QueryRow(context.Background(),
		`SELECT media_ttl_seconds, video_ttl_seconds, knowledge_ttl_seconds, updated_at, updated_by
		 FROM retention_policy WHERE id='default'`,
	).Scan(&p.MediaTTLSeconds, &p.VideoTTLSeconds, &p.KnowledgeTTLSeconds, &p.UpdatedAt, &p.UpdatedBy)
	if err != nil {
		return DefaultRetentionPolicy(), nil
	}
	return p, nil
}

func (s *PGStore) SetRetentionPolicy(policy RetentionPolicy) error {
	policy = policy.normalize()
	policy.UpdatedAt = time.Now().UTC()
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO retention_policy (id, media_ttl_seconds, video_ttl_seconds, knowledge_ttl_seconds, updated_at, updated_by)
		 VALUES ('default',$1,$2,$3,$4,$5)
		 ON CONFLICT (id) DO UPDATE SET
		 media_ttl_seconds=EXCLUDED.media_ttl_seconds,
		 video_ttl_seconds=EXCLUDED.video_ttl_seconds,
		 knowledge_ttl_seconds=EXCLUDED.knowledge_ttl_seconds,
		 updated_at=EXCLUDED.updated_at,
		 updated_by=EXCLUDED.updated_by`,
		policy.MediaTTLSeconds, policy.VideoTTLSeconds, policy.KnowledgeTTLSeconds, policy.UpdatedAt, policy.UpdatedBy,
	)
	return err
}

func (s *PGStore) PurgeMediaKinds(meetingID string, kinds []string) ([]MediaArtifact, error) {
	list, err := s.ListMediaArtifacts(meetingID)
	if err != nil {
		return nil, err
	}
	out := make([]MediaArtifact, 0)
	for _, art := range list {
		if art.Status == "purged" || !mediaKindWanted(art.Kind, kinds) {
			continue
		}
		out = append(out, art)
		if _, err := s.pool.Exec(context.Background(),
			`UPDATE media_artifacts SET status='purged', object_key='', detail='retention_expired' WHERE id=$1`,
			art.ID,
		); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *PGStore) PurgeKnowledge(meetingID string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM transcript_segments WHERE meeting_id=$1`, meetingID); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM summary_revisions WHERE meeting_id=$1`, meetingID); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM meeting_summaries WHERE meeting_id=$1`, meetingID); err != nil {
		return err
	}
	return s.SetPipelineStage(meetingID, StageKnowledgePurged)
}

var _ Repository = (*PGStore)(nil)
