package identity

const (
	KindEmployee = "employee"
	KindGuest    = "guest"
)

type Principal struct {
	UserID      string
	Kind        string
	Email       string
	DisplayName string
	Roles       []string
	MeetingID   string
	GuestID     string
}
