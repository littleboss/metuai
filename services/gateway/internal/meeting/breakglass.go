package meeting

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// BreakGlass 破窗听会/看产物（架构 §8.1）：申请 → 他人批准 → 时限内访问。
type BreakGlass interface {
	Apply(meetingID, applicant, reason string) (BreakGlassRequest, error)
	Approve(id, approver string, ttl time.Duration) (BreakGlassRequest, error)
	Deny(id, actor string) (BreakGlassRequest, error)
	Revoke(id, actor string) (BreakGlassRequest, error)
	ElevatedMeetingIDs(userID string) []string
	HasActiveGrant(meetingID, userID string) bool
	Get(id string) (BreakGlassRequest, bool)
	ListForMeeting(meetingID string) []BreakGlassRequest
}

// BreakGlassRequest 破窗申请记录。
type BreakGlassRequest struct {
	ID         string    `json:"id"`
	MeetingID  string    `json:"meeting_id"`
	Applicant  string    `json:"applicant"` // employee user id
	Reason     string    `json:"reason"`
	Status     string    `json:"status"` // pending | approved | denied | expired
	Approver   string    `json:"approver,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	ApprovedAt time.Time `json:"approved_at,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
}

// BreakGlassStore 进程内实现（无 DATABASE_URL 时使用）。
type BreakGlassStore struct {
	mu   sync.RWMutex
	reqs map[string]BreakGlassRequest
}

func NewBreakGlassStore() *BreakGlassStore {
	return &BreakGlassStore{reqs: map[string]BreakGlassRequest{}}
}

func (s *BreakGlassStore) Apply(meetingID, applicant, reason string) (BreakGlassRequest, error) {
	if reason == "" {
		return BreakGlassRequest{}, fmt.Errorf("reason_required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.reqs[id] = req
	return req, nil
}

func (s *BreakGlassStore) Approve(id, approver string, ttl time.Duration) (BreakGlassRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.reqs[id]
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
	s.reqs[id] = req
	return req, nil
}

func (s *BreakGlassStore) Deny(id, actor string) (BreakGlassRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.reqs[id]
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
	s.reqs[id] = req
	return req, nil
}

func (s *BreakGlassStore) Revoke(id, actor string) (BreakGlassRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.reqs[id]
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
	s.reqs[id] = req
	return req, nil
}

func (s *BreakGlassStore) ElevatedMeetingIDs(userID string) []string {
	if s == nil || userID == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, req := range s.reqs {
		if req.Status != "approved" || req.Applicant != userID {
			continue
		}
		if !req.ExpiresAt.IsZero() && now.After(req.ExpiresAt) {
			continue
		}
		if _, ok := seen[req.MeetingID]; ok {
			continue
		}
		seen[req.MeetingID] = struct{}{}
		out = append(out, req.MeetingID)
	}
	return out
}

func (s *BreakGlassStore) Get(id string) (BreakGlassRequest, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	req, ok := s.reqs[id]
	return req, ok
}

func (s *BreakGlassStore) ListForMeeting(meetingID string) []BreakGlassRequest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]BreakGlassRequest, 0)
	for _, req := range s.reqs {
		if req.MeetingID == meetingID {
			out = append(out, req)
		}
	}
	return out
}

func (s *BreakGlassStore) HasActiveGrant(meetingID, userID string) bool {
	for _, id := range s.ElevatedMeetingIDs(userID) {
		if id == meetingID {
			return true
		}
	}
	return false
}

// BreakGlassForRepo：有 PG 时落库，否则内存。
func BreakGlassForRepo(repo Repository) BreakGlass {
	if pg, ok := repo.(*PGStore); ok {
		return NewPGBreakGlass(pg)
	}
	return NewBreakGlassStore()
}
