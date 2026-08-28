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
	StartsAt      *time.Time
	EndsAt        *time.Time
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

// GuestEmailChallenge 是嘉宾会后访问的邮箱验证码/魔法链接挑战。
// CodeHash 与 TokenHash 永不返回客户端。
type GuestEmailChallenge struct {
	MeetingID  string
	GuestID    string
	Email      string
	CodeHash   string
	TokenHash  string
	ExpiresAt  time.Time
	Attempts   int
	VerifiedAt *time.Time
	CreatedAt  time.Time
}

// GuestIdentityRef 记录某封邮箱曾经用过的临时嘉宾 ID，供跨会合并。
type GuestIdentityRef struct {
	MeetingID string
	GuestID   string
}

// GuestParticipant 是已确认录音的会中嘉宾，带入会时的显示名快照。
type GuestParticipant struct {
	GuestID     string `json:"guest_id"`
	DisplayName string `json:"display_name"`
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

// CitedItem 是带转写引用的纪要条目（决策、风险、待澄清问题）。
type CitedItem struct {
	Text             string   `json:"text"`
	SourceSegmentIDs []string `json:"source_segment_ids,omitempty"`
}

// ActionItem 对齐架构 §6.3：负责人只能是内部员工，嘉宾只能写在任务描述里。
type ActionItem struct {
	Task             string     `json:"task"`
	OwnerUserID      string     `json:"owner_user_id,omitempty"`
	Deadline         *string    `json:"deadline,omitempty"`
	SourceSegmentIDs []string   `json:"source_segment_ids,omitempty"`
	SourceMessageIDs []string   `json:"source_message_ids,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
}

// MeetingSummary 对齐架构 §6.3 的结构化纪要。
// OriginalJSON 是 AI 原稿，修订时不得覆盖。Model 记录生成所用私有 LLM。
type MeetingSummary struct {
	MeetingID     string       `json:"meeting_id"`
	Summary       string       `json:"summary"`
	Decisions     []CitedItem  `json:"decisions"`
	ActionItems   []ActionItem `json:"action_items"`
	Risks         []CitedItem  `json:"risks"`
	OpenQuestions []CitedItem  `json:"open_questions"`
	OriginalJSON  string       `json:"original_json,omitempty"`
	Model         string       `json:"model,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	RevisedAt     *time.Time   `json:"revised_at,omitzero"`
}

// SummaryRevision 是只追加的纪要修订事件（谁、何时、改了什么）。
type SummaryRevision struct {
	ID        string    `json:"id"`
	MeetingID string    `json:"meeting_id"`
	ActorKey  string    `json:"actor_key"`
	PatchJSON string    `json:"patch_json"`
	CreatedAt time.Time `json:"created_at"`
}
