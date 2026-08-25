package meeting

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

const passwordChars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

type Repository interface {
	Create(title, organizerID, plainPassword string) (Meeting, string, error)
	Get(id string) (Meeting, bool)
	CheckPassword(id, plain string) bool
	SetLocked(id string, locked bool) error
	AckRecording(meetingID, principalKey string) error
	HasAck(meetingID, principalKey string) bool
}

type ackKey struct {
	meetingID string
	principal string
}

type Store struct {
	mu       sync.Mutex
	meetings map[string]Meeting
	acks     map[ackKey]struct{}
}

func NewMemoryStore() *Store {
	return &Store{
		meetings: map[string]Meeting{},
		acks:     map[ackKey]struct{}{},
	}
}

func HashPassword(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func randomPassword() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 8)
	for i := range out {
		out[i] = passwordChars[int(b[i])%len(passwordChars)]
	}
	return string(out), nil
}

func RandomID(prefix string) (string, error) {
	randomPart, err := randomPassword()
	if err != nil {
		return "", err
	}
	return prefix + randomPart, nil
}

func (s *Store) Create(title, organizerID, plainPassword string) (Meeting, string, error) {
	if title == "" {
		title = "即时会议"
	}
	plain := plainPassword
	var err error
	if plain == "" {
		plain, err = randomPassword()
		if err != nil {
			return Meeting{}, "", err
		}
	}
	id, err := RandomID("mtg_")
	if err != nil {
		return Meeting{}, "", err
	}
	m := Meeting{
		ID:           id,
		Title:        title,
		PasswordHash: HashPassword(plain),
		OrganizerID:  organizerID,
		Locked:       false,
		CreatedAt:    time.Now().UTC(),
	}
	s.mu.Lock()
	s.meetings[m.ID] = m
	s.mu.Unlock()
	return m, plain, nil
}

func (s *Store) Get(id string) (Meeting, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.meetings[id]
	return m, ok
}

func (s *Store) CheckPassword(id, plain string) bool {
	m, ok := s.Get(id)
	if !ok {
		return false
	}
	return m.PasswordHash == HashPassword(plain)
}

func (s *Store) SetLocked(id string, locked bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.meetings[id]
	if !ok {
		return fmt.Errorf("meeting not found")
	}
	m.Locked = locked
	s.meetings[id] = m
	return nil
}

func (s *Store) AckRecording(meetingID, principalKey string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	s.mu.Lock()
	s.acks[ackKey{meetingID: meetingID, principal: principalKey}] = struct{}{}
	s.mu.Unlock()
	return nil
}

func (s *Store) HasAck(meetingID, principalKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.acks[ackKey{meetingID: meetingID, principal: principalKey}]
	return ok
}

func PrincipalKey(kind, userID, guestID string) string {
	if kind == "guest" {
		return "guest:" + guestID
	}
	return "employee:" + userID
}

var _ Repository = (*Store)(nil)
