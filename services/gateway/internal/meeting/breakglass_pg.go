package meeting

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const breakGlassSchema = `
CREATE TABLE IF NOT EXISTS break_glass_requests (
  id TEXT PRIMARY KEY,
  meeting_id TEXT NOT NULL,
  applicant TEXT NOT NULL,
  reason TEXT NOT NULL,
  status TEXT NOT NULL,
  approver TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL,
  approved_at TIMESTAMPTZ,
  expires_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS break_glass_meeting_idx ON break_glass_requests (meeting_id, created_at);
CREATE INDEX IF NOT EXISTS break_glass_applicant_idx ON break_glass_requests (applicant, status);
`

// PGBreakGlass 把破窗申请持久化到 PostgreSQL。
type PGBreakGlass struct {
	pg *PGStore
}

func NewPGBreakGlass(pg *PGStore) *PGBreakGlass {
	if pg != nil && pg.pool != nil {
		_, _ = pg.pool.Exec(context.Background(), breakGlassSchema)
	}
	return &PGBreakGlass{pg: pg}
}

func (s *PGBreakGlass) Apply(meetingID, applicant, reason string) (BreakGlassRequest, error) {
	if reason == "" {
		return BreakGlassRequest{}, fmt.Errorf("reason_required")
	}
	id, err := RandomID("bg_")
	if err != nil {
		return BreakGlassRequest{}, err
	}
	req := BreakGlassRequest{
		ID:        id,
		MeetingID: meetingID,
		Applicant: applicant,
		Reason:    reason,
		Status:    "pending",
		CreatedAt: time.Now().UTC(),
	}
	_, err = s.pg.pool.Exec(context.Background(),
		`INSERT INTO break_glass_requests (id, meeting_id, applicant, reason, status, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		req.ID, req.MeetingID, req.Applicant, req.Reason, req.Status, req.CreatedAt,
	)
	if err != nil {
		return BreakGlassRequest{}, err
	}
	return req, nil
}

func (s *PGBreakGlass) Approve(id, approver string, ttl time.Duration) (BreakGlassRequest, error) {
	req, ok := s.Get(id)
	if !ok {
		return BreakGlassRequest{}, fmt.Errorf("not_found")
	}
	if req.Status != "pending" {
		return BreakGlassRequest{}, fmt.Errorf("not_pending")
	}
	if approver == "" || approver == req.Applicant {
		return BreakGlassRequest{}, fmt.Errorf("cannot_self_approve")
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	now := time.Now().UTC()
	req.Status = "approved"
	req.Approver = approver
	req.ApprovedAt = now
	req.ExpiresAt = now.Add(ttl)
	_, err := s.pg.pool.Exec(context.Background(),
		`UPDATE break_glass_requests
		 SET status=$2, approver=$3, approved_at=$4, expires_at=$5
		 WHERE id=$1`,
		req.ID, req.Status, req.Approver, req.ApprovedAt, req.ExpiresAt,
	)
	if err != nil {
		return BreakGlassRequest{}, err
	}
	return req, nil
}

func (s *PGBreakGlass) Deny(id, actor string) (BreakGlassRequest, error) {
	req, ok := s.Get(id)
	if !ok {
		return BreakGlassRequest{}, fmt.Errorf("not_found")
	}
	if req.Status != "pending" {
		return BreakGlassRequest{}, fmt.Errorf("not_pending")
	}
	if strings.TrimSpace(actor) == "" {
		return BreakGlassRequest{}, fmt.Errorf("actor_required")
	}
	req.Status = "denied"
	req.Approver = actor
	_, err := s.pg.pool.Exec(context.Background(),
		`UPDATE break_glass_requests SET status=$2, approver=$3 WHERE id=$1`,
		req.ID, req.Status, req.Approver,
	)
	if err != nil {
		return BreakGlassRequest{}, err
	}
	return req, nil
}

func (s *PGBreakGlass) Revoke(id, actor string) (BreakGlassRequest, error) {
	req, ok := s.Get(id)
	if !ok {
		return BreakGlassRequest{}, fmt.Errorf("not_found")
	}
	if req.Status != "approved" {
		return BreakGlassRequest{}, fmt.Errorf("not_approved")
	}
	if strings.TrimSpace(actor) == "" {
		return BreakGlassRequest{}, fmt.Errorf("actor_required")
	}
	now := time.Now().UTC()
	req.Status = "expired"
	req.Approver = actor
	req.ExpiresAt = now
	_, err := s.pg.pool.Exec(context.Background(),
		`UPDATE break_glass_requests SET status=$2, approver=$3, expires_at=$4 WHERE id=$1`,
		req.ID, req.Status, req.Approver, req.ExpiresAt,
	)
	if err != nil {
		return BreakGlassRequest{}, err
	}
	return req, nil
}

func (s *PGBreakGlass) Get(id string) (BreakGlassRequest, bool) {
	var req BreakGlassRequest
	var approvedAt, expiresAt *time.Time
	err := s.pg.pool.QueryRow(context.Background(),
		`SELECT id, meeting_id, applicant, reason, status, approver, created_at, approved_at, expires_at
		 FROM break_glass_requests WHERE id=$1`, id,
	).Scan(&req.ID, &req.MeetingID, &req.Applicant, &req.Reason, &req.Status, &req.Approver,
		&req.CreatedAt, &approvedAt, &expiresAt)
	if err != nil {
		return BreakGlassRequest{}, false
	}
	if approvedAt != nil {
		req.ApprovedAt = *approvedAt
	}
	if expiresAt != nil {
		req.ExpiresAt = *expiresAt
	}
	return req, true
}

func (s *PGBreakGlass) ListForMeeting(meetingID string) []BreakGlassRequest {
	rows, err := s.pg.pool.Query(context.Background(),
		`SELECT id, meeting_id, applicant, reason, status, approver, created_at, approved_at, expires_at
		 FROM break_glass_requests WHERE meeting_id=$1 ORDER BY created_at`, meetingID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]BreakGlassRequest, 0)
	for rows.Next() {
		var req BreakGlassRequest
		var approvedAt, expiresAt *time.Time
		if err := rows.Scan(&req.ID, &req.MeetingID, &req.Applicant, &req.Reason, &req.Status, &req.Approver,
			&req.CreatedAt, &approvedAt, &expiresAt); err != nil {
			continue
		}
		if approvedAt != nil {
			req.ApprovedAt = *approvedAt
		}
		if expiresAt != nil {
			req.ExpiresAt = *expiresAt
		}
		out = append(out, req)
	}
	return out
}

func (s *PGBreakGlass) ElevatedMeetingIDs(userID string) []string {
	if userID == "" {
		return nil
	}
	rows, err := s.pg.pool.Query(context.Background(),
		`SELECT DISTINCT meeting_id FROM break_glass_requests
		 WHERE applicant=$1 AND status='approved'
		   AND (expires_at IS NULL OR expires_at > NOW())`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		out = append(out, id)
	}
	return out
}

func (s *PGBreakGlass) HasActiveGrant(meetingID, userID string) bool {
	for _, id := range s.ElevatedMeetingIDs(userID) {
		if id == meetingID {
			return true
		}
	}
	return false
}
