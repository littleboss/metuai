package meeting

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	lktoken "metuai/services/gateway/internal/livekit"

	"golang.org/x/crypto/bcrypt"
)

const passwordChars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

type Repository interface {
	Create(title, organizerID, plainPassword string) (Meeting, string, error)
	Get(id string) (Meeting, bool)
	AddMembers(meetingID string, members []MeetingMember) error
	MarkMemberJoined(meetingID, userID, displayName string) error
	IsInvitedEmployee(meetingID, userID string) bool
	IsOrganizerOrCoOrganizer(meetingID, userID string) bool
	ListMeetingsForEmployee(userID string) ([]Meeting, error)
	ListActive() ([]Meeting, error)
	ListEnded() ([]Meeting, error)
	ListEndedNeedingPipeline() ([]Meeting, error)
	CheckPassword(id, plain string) bool
	SetLocked(id string, locked bool) error
	ResetPassword(id string) (string, error)
	End(id string) error
	TouchActivity(id string) error
	SetPipelineStage(id, stage string) error
	AckRecording(meetingID, principalKey string) error
	HasAck(meetingID, principalKey string) bool
	Kick(meetingID, identity string) error
	IsKicked(meetingID, identity string) bool
	AddChat(msg ChatMessage) (ChatMessage, error)
	ListChat(meetingID string) ([]ChatMessage, error)
	AppendAudit(event AuditEvent) error
	ListAudit(meetingID string) ([]AuditEvent, error)
	AddMediaArtifact(a MediaArtifact) (MediaArtifact, error)
	ListMediaArtifacts(meetingID string) ([]MediaArtifact, error)
	UpdateMediaArtifactStatus(id, status, detail string) error
	ReplaceTranscript(meetingID string, segments []TranscriptSegment) error
	ListTranscript(meetingID string) ([]TranscriptSegment, error)
	UpsertSummary(summary MeetingSummary) error
	GetSummary(meetingID string) (MeetingSummary, bool)
	AddGuestEmail(meetingID, email string) error
	AddGuestEmailSource(meetingID, email, source string) error
	ListGuestEmails(meetingID string) ([]string, error)
	AddShare(meetingID, email, createdBy string) error
	ListShares(meetingID string) ([]MeetingShare, error)
	RemoveShare(meetingID, email string) error
	HasShare(meetingID, email string) bool
	SaveGuestEmailChallenge(challenge GuestEmailChallenge) error
	VerifyGuestEmailChallenge(meetingID, guestID, email, code string) (GuestEmailChallenge, error)
	VerifyGuestMagicToken(meetingID, token string) (GuestEmailChallenge, error)
	ListMeetingIDsForGuestEmail(email string) ([]string, error)
	ListGuestIdentitiesForEmail(email string) ([]GuestIdentityRef, error)
	RewriteIdentityKeys(meetingID string, fromKeys []string, toKey string) error
	SavePipelineTask(task PipelineTask) (PipelineTask, error)
	ClaimPipelineTasks(owner, kind string, limit int) ([]PipelineTask, error)
	GetPipelineTask(id string) (PipelineTask, bool)
	UpdatePipelineTask(task PipelineTask) error
	ListPipelineTasks(meetingID string) ([]PipelineTask, error)
	ListEmployeeParticipantIDs(meetingID string) ([]string, error)
	ListGuestParticipantIDs(meetingID string) ([]string, error)
	UpsertGuestPresence(meetingID, guestID, displayName string) error
	ListGuestParticipants(meetingID string) ([]GuestParticipant, error)
	ListMembers(meetingID string) ([]MeetingMember, error)
	AppendSummaryRevision(rev SummaryRevision) (SummaryRevision, error)
	ListSummaryRevisions(meetingID string) ([]SummaryRevision, error)
	GetRetentionPolicy() (RetentionPolicy, error)
	SetRetentionPolicy(policy RetentionPolicy) error
	PurgeMediaKinds(meetingID string, kinds []string) ([]MediaArtifact, error)
	PurgeKnowledge(meetingID string) error
}

type ackKey struct {
	meetingID string
	principal string
}

type kickKey struct {
	meetingID string
	identity  string
}

type guestChallengeKey struct {
	meetingID string
	guestID   string
}

type Store struct {
	mu              sync.Mutex
	meetings        map[string]Meeting
	acks            map[ackKey]struct{}
	kicks           map[kickKey]struct{}
	chats           map[string][]ChatMessage
	media           map[string][]MediaArtifact
	transcripts     map[string][]TranscriptSegment
	summaries       map[string]MeetingSummary
	audits          []AuditEvent
	guestEmails     map[string]map[string]string
	shares          map[string]map[string]MeetingShare
	members         map[string]map[string]MeetingMember
	guestChallenges map[guestChallengeKey]GuestEmailChallenge
	guestPresence   map[string]map[string]string
	revisions       map[string][]SummaryRevision
	retention       RetentionPolicy
	pipelineTasks   map[string]PipelineTask
}

func NewMemoryStore() *Store {
	return &Store{
		meetings:        map[string]Meeting{},
		acks:            map[ackKey]struct{}{},
		kicks:           map[kickKey]struct{}{},
		chats:           map[string][]ChatMessage{},
		media:           map[string][]MediaArtifact{},
		transcripts:     map[string][]TranscriptSegment{},
		summaries:       map[string]MeetingSummary{},
		guestEmails:     map[string]map[string]string{},
		shares:          map[string]map[string]MeetingShare{},
		members:         map[string]map[string]MeetingMember{},
		guestChallenges: map[guestChallengeKey]GuestEmailChallenge{},
		guestPresence:   map[string]map[string]string{},
		revisions:       map[string][]SummaryRevision{},
		retention:       DefaultRetentionPolicy(),
		pipelineTasks:   map[string]PipelineTask{},
	}
}

func legacyPasswordHash(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func hashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

func passwordMatches(hash, plain string) bool {
	if strings.HasPrefix(hash, "$2") {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
	}
	legacy := legacyPasswordHash(plain)
	return subtle.ConstantTimeCompare([]byte(hash), []byte(legacy)) == 1
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
	passwordHash, err := hashPassword(plain)
	if err != nil {
		return Meeting{}, "", err
	}
	now := time.Now().UTC()
	m := Meeting{
		ID:            id,
		Title:         title,
		PasswordHash:  passwordHash,
		OrganizerID:   organizerID,
		Locked:        false,
		Ended:         false,
		LastActiveAt:  now,
		PipelineStage: "",
		CreatedAt:     now,
	}
	s.mu.Lock()
	s.meetings[m.ID] = m
	s.members[m.ID] = map[string]MeetingMember{
		organizerID: {
			MeetingID: m.ID,
			UserID:    organizerID,
			Role:      MeetingMemberOrganizer,
			InvitedAt: now,
		},
	}
	s.mu.Unlock()
	return m, plain, nil
}

func (s *Store) Get(id string) (Meeting, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.meetings[id]
	return m, ok
}

func memberRolePriority(role MeetingMemberRole) int {
	switch role {
	case MeetingMemberOrganizer:
		return 3
	case MeetingMemberCoOrganizer:
		return 2
	case MeetingMemberInvited:
		return 1
	case MeetingMemberParticipant:
		return 0
	default:
		return -1
	}
}

func (s *Store) AddMembers(meetingID string, members []MeetingMember) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.meetings[meetingID]; !ok {
		return fmt.Errorf("meeting not found")
	}
	if s.members[meetingID] == nil {
		s.members[meetingID] = map[string]MeetingMember{}
	}
	now := time.Now().UTC()
	for _, member := range members {
		member.UserID = strings.TrimSpace(member.UserID)
		if member.UserID == "" {
			continue
		}
		if memberRolePriority(member.Role) < 0 {
			return fmt.Errorf("invalid meeting member role")
		}
		member.MeetingID = meetingID
		if member.InvitedAt.IsZero() {
			member.InvitedAt = now
		}
		existing, exists := s.members[meetingID][member.UserID]
		if exists {
			if memberRolePriority(existing.Role) > memberRolePriority(member.Role) {
				member.Role = existing.Role
			}
			if member.DisplayNameSnapshot == "" {
				member.DisplayNameSnapshot = existing.DisplayNameSnapshot
			}
			member.InvitedAt = existing.InvitedAt
			member.JoinedAt = existing.JoinedAt
		}
		s.members[meetingID][member.UserID] = member
	}
	return nil
}

func (s *Store) MarkMemberJoined(meetingID, userID, displayName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.meetings[meetingID]; !ok {
		return fmt.Errorf("meeting not found")
	}
	if s.members[meetingID] == nil {
		s.members[meetingID] = map[string]MeetingMember{}
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("employee user id required")
	}
	member, ok := s.members[meetingID][userID]
	if !ok {
		now := time.Now().UTC()
		member = MeetingMember{
			MeetingID: meetingID,
			UserID:    userID,
			Role:      MeetingMemberParticipant,
			InvitedAt: now,
		}
	}
	if member.DisplayNameSnapshot == "" {
		member.DisplayNameSnapshot = strings.TrimSpace(displayName)
	}
	if member.JoinedAt == nil {
		now := time.Now().UTC()
		member.JoinedAt = &now
	}
	s.members[meetingID][userID] = member
	return nil
}

func (s *Store) IsInvitedEmployee(meetingID, userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.members[meetingID][userID]
	return ok && member.Role != MeetingMemberParticipant
}

func (s *Store) IsOrganizerOrCoOrganizer(meetingID, userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.members[meetingID][userID]
	return ok && (member.Role == MeetingMemberOrganizer || member.Role == MeetingMemberCoOrganizer)
}

func (s *Store) ListMeetingsForEmployee(userID string) ([]Meeting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meeting, 0)
	for meetingID := range s.members {
		if _, ok := s.members[meetingID][userID]; !ok {
			continue
		}
		if meeting, ok := s.meetings[meetingID]; ok {
			out = append(out, meeting)
		}
	}
	slices.SortFunc(out, func(a, b Meeting) int {
		return b.CreatedAt.Compare(a.CreatedAt)
	})
	return out, nil
}

func (s *Store) ListActive() ([]Meeting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meeting, 0)
	for _, m := range s.meetings {
		if !m.Ended {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Store) ListEnded() ([]Meeting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meeting, 0)
	for _, m := range s.meetings {
		if m.Ended {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Store) ListEndedNeedingPipeline() ([]Meeting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meeting, 0)
	for _, m := range s.meetings {
		if m.Ended && m.PipelineStage != StageReady && m.PipelineStage != StageManualReview {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Store) CheckPassword(id, plain string) bool {
	m, ok := s.Get(id)
	if !ok {
		return false
	}
	return passwordMatches(m.PasswordHash, plain)
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

func (s *Store) ResetPassword(id string) (string, error) {
	plain, err := randomPassword()
	if err != nil {
		return "", err
	}
	passwordHash, err := hashPassword(plain)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.meetings[id]
	if !ok {
		return "", fmt.Errorf("meeting not found")
	}
	m.PasswordHash = passwordHash
	s.meetings[id] = m
	return plain, nil
}

func (s *Store) End(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.meetings[id]
	if !ok {
		return fmt.Errorf("meeting not found")
	}
	now := time.Now().UTC()
	m.Ended = true
	m.EndedAt = &now
	if m.PipelineStage == "" {
		m.PipelineStage = StageRecordingFinalized
	}
	s.meetings[id] = m
	return nil
}

func (s *Store) TouchActivity(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.meetings[id]
	if !ok {
		return fmt.Errorf("meeting not found")
	}
	m.LastActiveAt = time.Now().UTC()
	s.meetings[id] = m
	return nil
}

func (s *Store) SetPipelineStage(id, stage string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.meetings[id]
	if !ok {
		return fmt.Errorf("meeting not found")
	}
	m.PipelineStage = stage
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

func (s *Store) Kick(meetingID, identity string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	identity = lktoken.UserKey(identity)
	s.mu.Lock()
	s.kicks[kickKey{meetingID: meetingID, identity: identity}] = struct{}{}
	s.mu.Unlock()
	return nil
}

func (s *Store) IsKicked(meetingID, identity string) bool {
	identity = lktoken.UserKey(identity)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.kicks[kickKey{meetingID: meetingID, identity: identity}]
	return ok
}

func (s *Store) AddChat(msg ChatMessage) (ChatMessage, error) {
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
	s.mu.Lock()
	s.chats[msg.MeetingID] = append(s.chats[msg.MeetingID], msg)
	s.mu.Unlock()
	return msg, nil
}

func (s *Store) ListChat(meetingID string) ([]ChatMessage, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.chats[meetingID]
	out := make([]ChatMessage, len(src))
	copy(out, src)
	return out, nil
}

func (s *Store) AppendAudit(event AuditEvent) error {
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
	s.mu.Lock()
	s.audits = append(s.audits, event)
	s.mu.Unlock()
	return nil
}

func (s *Store) ListAudit(meetingID string) ([]AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AuditEvent, 0)
	for _, e := range s.audits {
		if e.MeetingID == meetingID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Store) AddMediaArtifact(a MediaArtifact) (MediaArtifact, error) {
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
	s.mu.Lock()
	s.media[a.MeetingID] = append(s.media[a.MeetingID], a)
	s.mu.Unlock()
	return a, nil
}

func (s *Store) ListMediaArtifacts(meetingID string) ([]MediaArtifact, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.media[meetingID]
	out := make([]MediaArtifact, len(src))
	copy(out, src)
	return out, nil
}

func (s *Store) UpdateMediaArtifactStatus(id, status, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for meetingID, list := range s.media {
		for i := range list {
			if list[i].ID == id {
				list[i].Status = status
				list[i].Detail = detail
				s.media[meetingID] = list
				return nil
			}
		}
	}
	return fmt.Errorf("media artifact not found")
}

func (s *Store) ReplaceTranscript(meetingID string, segments []TranscriptSegment) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	out := make([]TranscriptSegment, 0, len(segments))
	for _, seg := range segments {
		if seg.ID == "" {
			id, err := RandomID("seg_")
			if err != nil {
				return err
			}
			seg.ID = id
		}
		seg.MeetingID = meetingID
		out = append(out, seg)
	}
	s.mu.Lock()
	s.transcripts[meetingID] = out
	s.mu.Unlock()
	return nil
}

func (s *Store) ListTranscript(meetingID string) ([]TranscriptSegment, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.transcripts[meetingID]
	out := make([]TranscriptSegment, len(src))
	copy(out, src)
	return out, nil
}

func (s *Store) UpsertSummary(summary MeetingSummary) error {
	if _, ok := s.Get(summary.MeetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	if summary.CreatedAt.IsZero() {
		summary.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.summaries[summary.MeetingID]; ok {
		if summary.OriginalJSON == "" {
			summary.OriginalJSON = existing.OriginalJSON
		}
		if !existing.CreatedAt.IsZero() {
			summary.CreatedAt = existing.CreatedAt
		}
	} else if summary.OriginalJSON == "" {
		summary.OriginalJSON = captureOriginalJSON(summary)
	}
	s.summaries[summary.MeetingID] = summary
	return nil
}

func (s *Store) GetSummary(meetingID string) (MeetingSummary, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum, ok := s.summaries[meetingID]
	return sum, ok
}

func (s *Store) AddGuestEmail(meetingID, email string) error {
	return s.AddGuestEmailSource(meetingID, email, guestEmailSourceParticipant)
}

func (s *Store) AddGuestEmailSource(meetingID, email, source string) error {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.guestEmails[meetingID] == nil {
		s.guestEmails[meetingID] = map[string]string{}
	}
	existing := s.guestEmails[meetingID][email]
	if existing == guestEmailSourceParticipant {
		return nil
	}
	s.guestEmails[meetingID][email] = source
	return nil
}

func (s *Store) ListGuestEmails(meetingID string) ([]string, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.guestEmails[meetingID]))
	for e := range s.guestEmails[meetingID] {
		out = append(out, e)
	}
	return out, nil
}

func (s *Store) AddShare(meetingID, email, createdBy string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	email = normalizeGuestEmail(email)
	if email == "" {
		return fmt.Errorf("invalid_email")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shares[meetingID] == nil {
		s.shares[meetingID] = map[string]MeetingShare{}
	}
	if _, exists := s.shares[meetingID][email]; exists {
		return nil
	}
	s.shares[meetingID][email] = MeetingShare{
		Email:     email,
		CreatedBy: strings.TrimSpace(createdBy),
		CreatedAt: time.Now().UTC(),
	}
	return nil
}

func (s *Store) ListShares(meetingID string) ([]MeetingShare, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MeetingShare, 0, len(s.shares[meetingID]))
	for _, share := range s.shares[meetingID] {
		_, share.Verified = s.guestEmails[meetingID][share.Email]
		out = append(out, share)
	}
	slices.SortFunc(out, func(a, b MeetingShare) int {
		return cmp.Compare(a.Email, b.Email)
	})
	return out, nil
}

func (s *Store) RemoveShare(meetingID, email string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	email = normalizeGuestEmail(email)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shares[meetingID] != nil {
		delete(s.shares[meetingID], email)
	}
	if s.guestEmails[meetingID] != nil && s.guestEmails[meetingID][email] == guestEmailSourceShared {
		delete(s.guestEmails[meetingID], email)
	}
	return nil
}

func (s *Store) HasShare(meetingID, email string) bool {
	email = normalizeGuestEmail(email)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.shares[meetingID][email]
	return ok
}

func (s *Store) SaveGuestEmailChallenge(challenge GuestEmailChallenge) error {
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
	s.mu.Lock()
	s.guestChallenges[guestChallengeKey{meetingID: challenge.MeetingID, guestID: challenge.GuestID}] = challenge
	s.mu.Unlock()
	return nil
}

func (s *Store) VerifyGuestEmailChallenge(meetingID, guestID, email, code string) (GuestEmailChallenge, error) {
	email = normalizeGuestEmail(email)
	s.mu.Lock()
	defer s.mu.Unlock()
	key := guestChallengeKey{meetingID: meetingID, guestID: strings.TrimSpace(guestID)}
	challenge, ok := s.guestChallenges[key]
	if !ok || challenge.Email != email {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationInvalid
	}
	if challenge.VerifiedAt != nil {
		return challenge, nil
	}
	if time.Now().UTC().After(challenge.ExpiresAt) {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationExpired
	}
	if challenge.Attempts >= guestEmailVerificationMaxAttempts {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationAttemptsExceeded
	}
	if bcrypt.CompareHashAndPassword([]byte(challenge.CodeHash), []byte(strings.TrimSpace(code))) != nil {
		challenge.Attempts++
		s.guestChallenges[key] = challenge
		return GuestEmailChallenge{}, ErrGuestEmailVerificationInvalid
	}
	now := time.Now().UTC()
	challenge.VerifiedAt = &now
	s.guestChallenges[key] = challenge
	return challenge, nil
}

func (s *Store) ListEmployeeParticipantIDs(meetingID string) ([]string, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for k := range s.acks {
		if k.meetingID != meetingID {
			continue
		}
		if !strings.HasPrefix(k.principal, "employee:") {
			continue
		}
		uid := strings.TrimPrefix(k.principal, "employee:")
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

func (s *Store) ListGuestParticipantIDs(meetingID string) ([]string, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for k := range s.acks {
		if k.meetingID != meetingID {
			continue
		}
		if !strings.HasPrefix(k.principal, "guest:") {
			continue
		}
		gid := strings.TrimPrefix(k.principal, "guest:")
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
	return out, nil
}

func (s *Store) UpsertGuestPresence(meetingID, guestID, displayName string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	guestID = strings.TrimSpace(strings.TrimPrefix(guestID, "guest:"))
	if guestID == "" {
		return fmt.Errorf("guest_id_required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.guestPresence[meetingID] == nil {
		s.guestPresence[meetingID] = map[string]string{}
	}
	name := strings.TrimSpace(displayName)
	if name == "" {
		return nil
	}
	s.guestPresence[meetingID][guestID] = name
	return nil
}

func (s *Store) ListGuestParticipants(meetingID string) ([]GuestParticipant, error) {
	ids, err := s.ListGuestParticipantIDs(meetingID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]GuestParticipant, 0, len(ids))
	for _, id := range ids {
		out = append(out, GuestParticipant{
			GuestID:     id,
			DisplayName: s.guestPresence[meetingID][id],
		})
	}
	return out, nil
}

func (s *Store) ListMembers(meetingID string) ([]MeetingMember, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MeetingMember, 0, len(s.members[meetingID]))
	for _, member := range s.members[meetingID] {
		out = append(out, member)
	}
	slices.SortFunc(out, func(a, b MeetingMember) int {
		return cmp.Compare(a.UserID, b.UserID)
	})
	return out, nil
}

func (s *Store) AppendSummaryRevision(rev SummaryRevision) (SummaryRevision, error) {
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
	s.mu.Lock()
	s.revisions[rev.MeetingID] = append(s.revisions[rev.MeetingID], rev)
	s.mu.Unlock()
	return rev, nil
}

func (s *Store) ListSummaryRevisions(meetingID string) ([]SummaryRevision, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.revisions[meetingID]
	out := make([]SummaryRevision, len(src))
	copy(out, src)
	return out, nil
}

func (s *Store) GetRetentionPolicy() (RetentionPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retention, nil
}

func (s *Store) SetRetentionPolicy(policy RetentionPolicy) error {
	policy = policy.normalize()
	policy.UpdatedAt = time.Now().UTC()
	s.mu.Lock()
	s.retention = policy
	s.mu.Unlock()
	return nil
}

func (s *Store) PurgeMediaKinds(meetingID string, kinds []string) ([]MediaArtifact, error) {
	if _, ok := s.Get(meetingID); !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.media[meetingID]
	out := make([]MediaArtifact, 0)
	for i := range list {
		if list[i].Status == "purged" || !mediaKindWanted(list[i].Kind, kinds) {
			continue
		}
		snapshot := list[i]
		list[i].Status = "purged"
		list[i].Detail = "retention_expired"
		list[i].ObjectKey = ""
		out = append(out, snapshot)
	}
	s.media[meetingID] = list
	return out, nil
}

func (s *Store) PurgeKnowledge(meetingID string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	s.mu.Lock()
	delete(s.transcripts, meetingID)
	delete(s.summaries, meetingID)
	delete(s.revisions, meetingID)
	m, ok := s.meetings[meetingID]
	if ok {
		m.PipelineStage = StageKnowledgePurged
		s.meetings[meetingID] = m
	}
	s.mu.Unlock()
	return nil
}

func (s *Store) VerifyGuestMagicToken(meetingID, token string) (GuestEmailChallenge, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationInvalid
	}
	want := hashMagicToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *guestChallengeKey
	var challenge GuestEmailChallenge
	for key, item := range s.guestChallenges {
		if key.meetingID != meetingID || item.TokenHash == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(item.TokenHash), []byte(want)) == 1 {
			copied := key
			found = &copied
			challenge = item
			break
		}
	}
	if found == nil {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationInvalid
	}
	if challenge.VerifiedAt != nil {
		return challenge, nil
	}
	if time.Now().UTC().After(challenge.ExpiresAt) {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationExpired
	}
	if challenge.Attempts >= guestEmailVerificationMaxAttempts {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationAttemptsExceeded
	}
	now := time.Now().UTC()
	challenge.VerifiedAt = &now
	s.guestChallenges[*found] = challenge
	return challenge, nil
}

func (s *Store) ListMeetingIDsForGuestEmail(email string) ([]string, error) {
	email = normalizeGuestEmail(email)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0)
	for meetingID, emails := range s.guestEmails {
		if _, ok := emails[email]; ok {
			out = append(out, meetingID)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (s *Store) ListGuestIdentitiesForEmail(email string) ([]GuestIdentityRef, error) {
	email = normalizeGuestEmail(email)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]GuestIdentityRef, 0)
	for key, challenge := range s.guestChallenges {
		if challenge.Email == email {
			out = append(out, GuestIdentityRef{MeetingID: key.meetingID, GuestID: key.guestID})
		}
	}
	slices.SortFunc(out, func(a, b GuestIdentityRef) int {
		if a.MeetingID != b.MeetingID {
			return cmp.Compare(a.MeetingID, b.MeetingID)
		}
		return cmp.Compare(a.GuestID, b.GuestID)
	})
	return out, nil
}

func (s *Store) RewriteIdentityKeys(meetingID string, fromKeys []string, toKey string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	toKey = strings.TrimSpace(toKey)
	if toKey == "" || len(fromKeys) == 0 {
		return nil
	}
	wanted := map[string]struct{}{}
	for _, key := range fromKeys {
		key = strings.TrimSpace(key)
		if key != "" && key != toKey {
			wanted[key] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	segs := s.transcripts[meetingID]
	for i := range segs {
		if _, ok := wanted[segs[i].SpeakerUserID]; ok {
			segs[i].SpeakerUserID = toKey
		}
	}
	s.transcripts[meetingID] = segs
	arts := s.media[meetingID]
	for i := range arts {
		if _, ok := wanted[arts[i].ParticipantKey]; ok {
			arts[i].ParticipantKey = toKey
		}
	}
	s.media[meetingID] = arts
	chats := s.chats[meetingID]
	for i := range chats {
		if _, ok := wanted[chats[i].SenderKey]; ok {
			chats[i].SenderKey = toKey
		}
	}
	s.chats[meetingID] = chats
	return nil
}

func (s *Store) SavePipelineTask(task PipelineTask) (PipelineTask, error) {
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
	s.mu.Lock()
	s.pipelineTasks[task.ID] = task
	s.mu.Unlock()
	return task, nil
}

func (s *Store) ClaimPipelineTasks(owner, kind string, limit int) ([]PipelineTask, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, fmt.Errorf("lease owner required")
	}
	if limit <= 0 {
		limit = 1
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(pipelineTaskLease)
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.pipelineTasks))
	for id := range s.pipelineTasks {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		return s.pipelineTasks[a].CreatedAt.Compare(s.pipelineTasks[b].CreatedAt)
	})
	out := make([]PipelineTask, 0, limit)
	for _, id := range ids {
		task := s.pipelineTasks[id]
		if kind != "" && task.Kind != kind {
			continue
		}
		if !pipelineTaskClaimable(task, now) {
			continue
		}
		task.Status = PipelineTaskLeased
		task.LeaseOwner = owner
		task.LeaseUntil = &leaseUntil
		task.UpdatedAt = now
		s.pipelineTasks[id] = task
		out = append(out, task)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *Store) GetPipelineTask(id string) (PipelineTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.pipelineTasks[id]
	return task, ok
}

func (s *Store) UpdatePipelineTask(task PipelineTask) error {
	if strings.TrimSpace(task.ID) == "" {
		return fmt.Errorf("task id required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pipelineTasks[task.ID]; !ok {
		return fmt.Errorf("pipeline task not found")
	}
	task.UpdatedAt = time.Now().UTC()
	s.pipelineTasks[task.ID] = task
	return nil
}

func (s *Store) ListPipelineTasks(meetingID string) ([]PipelineTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PipelineTask, 0)
	for _, task := range s.pipelineTasks {
		if meetingID != "" && task.MeetingID != meetingID {
			continue
		}
		out = append(out, task)
	}
	slices.SortFunc(out, func(a, b PipelineTask) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return out, nil
}

func PrincipalKey(kind, userID, guestID string) string {
	if kind == "guest" {
		return "guest:" + guestID
	}
	return "employee:" + userID
}

var _ Repository = (*Store)(nil)
