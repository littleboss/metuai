package meeting

import "time"

const (
	guestEmailSourceParticipant = "participant"
	guestEmailSourceShared      = "shared"
)

// MeetingShare 是组织者加入的会外读者白名单（架构 §7：任意邮箱须验证后才能读）。
type MeetingShare struct {
	Email     string    `json:"email"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	Verified  bool      `json:"verified"`
}

func shareGuestID(email string) string {
	return "share:" + normalizeGuestEmail(email)
}
