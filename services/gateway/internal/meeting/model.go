package meeting

import "time"

type Meeting struct {
	ID           string
	Title        string
	PasswordHash string
	OrganizerID  string
	Locked       bool
	CreatedAt    time.Time
}
