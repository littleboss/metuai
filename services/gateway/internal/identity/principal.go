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

func (p Principal) HasRole(role string) bool {
	for _, current := range p.Roles {
		if current == role {
			return true
		}
	}
	return false
}
