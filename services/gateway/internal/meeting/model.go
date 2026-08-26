package meeting

import "time"

// Meeting 是一场即时会议的权威状态（内存或 PostgreSQL）。
type Meeting struct {
	ID            string
	Title         string
	PasswordHash  string
	OrganizerID   string
	Locked        bool
	Ended         bool
	EndedAt       *time.Time
	LastActiveAt  time.Time
	PipelineStage string
	CreatedAt     time.Time
}

type MeetingMemberRole string

const (
	MeetingMemberOrganizer   MeetingMemberRole = "organizer"
	MeetingMemberCoOrganizer MeetingMemberRole = "co_organizer"
	MeetingMemberInvited     MeetingMemberRole = "invited"
	MeetingMemberParticipant MeetingMemberRole = "participant"
)

// MeetingMember 是员工会议邀请和权限的权威记录。Role 表示会议权限，JoinedAt 表示实际确认录音并加入。
type MeetingMember struct {
	MeetingID           string            `json:"meeting_id"`
	UserID              string            `json:"user_id"`
	Role                MeetingMemberRole `json:"role"`
	DisplayNameSnapshot string            `json:"display_name_snapshot"`
	InvitedAt           time.Time         `json:"invited_at"`
	JoinedAt            *time.Time        `json:"joined_at"`
}

// GuestEmailChallenge 是嘉宾会后访问的邮箱验证码挑战。CodeHash 永不返回客户端。
type GuestEmailChallenge struct {
	MeetingID  string
	GuestID    string
	Email      string
	CodeHash   string
	ExpiresAt  time.Time
	Attempts   int
	VerifiedAt *time.Time
	CreatedAt  time.Time
}

// ChatMessage 通过本系统 API 落库的会中聊天（不依赖 LiveKit 短暂数据通道）。
type ChatMessage struct {
	ID          string    `json:"id"`
	MeetingID   string    `json:"meeting_id"`
	SenderKey   string    `json:"sender_key"`
	DisplayName string    `json:"display_name"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
}

// AuditEvent 只追加、不可改删的审计记录。
type AuditEvent struct {
	ID        string    `json:"id"`
	MeetingID string    `json:"meeting_id"`
	ActorKey  string    `json:"actor_key"`
	Action    string    `json:"action"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

// TranscriptSegment 对齐架构 §6.2 的最小字段。
type TranscriptSegment struct {
	ID                 string   `json:"id"`
	MeetingID          string   `json:"meeting_id"`
	TrackID            string   `json:"track_id"`
	SpeakerUserID      string   `json:"speaker_user_id"`
	SpeakerDisplayName string   `json:"speaker_display_name"`
	Language           string   `json:"language"`
	StartMs            int      `json:"start_ms"`
	EndMs              int      `json:"end_ms"`
	Text               string   `json:"text"`
	ASRModel           string   `json:"asr_model"`
	Source             string   `json:"source"` // egress | local_fallback
	Confidence         *float64 `json:"confidence"`
}

// MeetingSummary 对齐架构 §6.3 的结构化纪要（PoC 用假数据填满字段）。
type MeetingSummary struct {
	MeetingID     string    `json:"meeting_id"`
	Summary       string    `json:"summary"`
	Decisions     []string  `json:"decisions"`
	ActionItems   []string  `json:"action_items"`
	Risks         []string  `json:"risks"`
	OpenQuestions []string  `json:"open_questions"`
	CreatedAt     time.Time `json:"created_at"`
}
