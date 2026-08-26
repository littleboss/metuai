package meeting

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
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
  created_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS meeting_guest_emails (
  meeting_id TEXT NOT NULL,
  email TEXT NOT NULL,
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
`)
	if err != nil {
		pool.Close()
		return nil, err
	}
	_, _ = pool.Exec(ctx, breakGlassSchema)
	return &PGStore{pool: pool}, nil
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
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO meeting_kicks (meeting_id, identity, kicked_at)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (meeting_id, identity) DO NOTHING`,
		meetingID, identity, time.Now().UTC(),
	)
	return err
}

func (s *PGStore) IsKicked(meetingID, identity string) bool {
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
		`INSERT INTO media_artifacts (id, meeting_id, kind, status, object_key, detail, egress_id, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		a.ID, a.MeetingID, a.Kind, a.Status, a.ObjectKey, a.Detail, a.EgressID, a.CreatedAt,
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
		`SELECT id, meeting_id, kind, status, object_key, detail, egress_id, created_at
		 FROM media_artifacts WHERE meeting_id=$1 ORDER BY created_at ASC`, meetingID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MediaArtifact, 0)
	for rows.Next() {
		var a MediaArtifact
		if err := rows.Scan(&a.ID, &a.MeetingID, &a.Kind, &a.Status, &a.ObjectKey, &a.Detail, &a.EgressID, &a.CreatedAt); err != nil {
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
	if summary.CreatedAt.IsZero() {
		summary.CreatedAt = time.Now().UTC()
	}
	dec, _ := json.Marshal(summary.Decisions)
	act, _ := json.Marshal(summary.ActionItems)
	risks, _ := json.Marshal(summary.Risks)
	openQ, _ := json.Marshal(summary.OpenQuestions)
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO meeting_summaries
		 (meeting_id, summary, decisions_json, action_items_json, risks_json, open_questions_json, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (meeting_id) DO UPDATE SET
		   summary=EXCLUDED.summary,
		   decisions_json=EXCLUDED.decisions_json,
		   action_items_json=EXCLUDED.action_items_json,
		   risks_json=EXCLUDED.risks_json,
		   open_questions_json=EXCLUDED.open_questions_json,
		   created_at=EXCLUDED.created_at`,
		summary.MeetingID, summary.Summary, string(dec), string(act), string(risks), string(openQ), summary.CreatedAt,
	)
	return err
}

func (s *PGStore) GetSummary(meetingID string) (MeetingSummary, bool) {
	var sum MeetingSummary
	var dec, act, risks, openQ string
	err := s.pool.QueryRow(context.Background(),
		`SELECT meeting_id, summary, decisions_json, action_items_json, risks_json, open_questions_json, created_at
		 FROM meeting_summaries WHERE meeting_id=$1`, meetingID,
	).Scan(&sum.MeetingID, &sum.Summary, &dec, &act, &risks, &openQ, &sum.CreatedAt)
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
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO meeting_guest_emails (meeting_id, email, created_at)
		 VALUES ($1,$2,NOW()) ON CONFLICT DO NOTHING`, meetingID, email)
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
		 (meeting_id, guest_id, email, code_hash, expires_at, attempts, verified_at, created_at)
		 VALUES ($1,$2,$3,$4,$5,0,NULL,$6)
		 ON CONFLICT (meeting_id, guest_id) DO UPDATE SET
		 email=EXCLUDED.email,
		 code_hash=EXCLUDED.code_hash,
		 expires_at=EXCLUDED.expires_at,
		 attempts=0,
		 verified_at=NULL,
		 created_at=EXCLUDED.created_at`,
		challenge.MeetingID, challenge.GuestID, challenge.Email, challenge.CodeHash, challenge.ExpiresAt, challenge.CreatedAt,
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

var _ Repository = (*PGStore)(nil)
