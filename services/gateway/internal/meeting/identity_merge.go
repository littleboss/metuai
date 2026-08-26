package meeting

import (
	"strings"
)

// CanonicalGuestSpeakerID 是验证邮箱后的稳定说话人身份（架构：临时 ID 合并到该邮箱）。
func CanonicalGuestSpeakerID(email string) string {
	email = normalizeGuestEmail(email)
	if email == "" {
		return ""
	}
	return "email:" + email
}

func guestIdentityAliases(guestID string) []string {
	guestID = strings.TrimSpace(guestID)
	if guestID == "" {
		return nil
	}
	out := []string{guestID, "guest:" + guestID}
	if strings.HasPrefix(guestID, "guest:") {
		out = append(out, strings.TrimPrefix(guestID, "guest:"))
	}
	return out
}

// MergeGuestIdentity 把本场及同一邮箱出现过的会议里的临时嘉宾 ID 改写成 email:地址。
func MergeGuestIdentity(repo Repository, meetingID, guestID, email string) error {
	canonical := CanonicalGuestSpeakerID(email)
	if canonical == "" {
		return nil
	}
	from := guestIdentityAliases(guestID)
	if refs, err := repo.ListGuestIdentitiesForEmail(email); err == nil {
		for _, ref := range refs {
			from = append(from, guestIdentityAliases(ref.GuestID)...)
		}
	}
	meetings := []string{meetingID}
	if ids, err := repo.ListMeetingIDsForGuestEmail(email); err == nil {
		meetings = append(meetings, ids...)
	}
	seenMeeting := map[string]struct{}{}
	for _, id := range meetings {
		if id == "" {
			continue
		}
		if _, ok := seenMeeting[id]; ok {
			continue
		}
		seenMeeting[id] = struct{}{}
		if err := repo.RewriteIdentityKeys(id, from, canonical); err != nil {
			return err
		}
	}
	return nil
}
