package meeting

import (
	"slices"
	"strings"
)

// DirectoryEmployee 是创建会议时可选的内部人员（PoC：来自当前员工见过的会议成员，不是真 IdP 目录）。
type DirectoryEmployee struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

func ListDirectoryEmployees(repo Repository, actorUserID, query string) ([]DirectoryEmployee, error) {
	meetings, err := repo.ListMeetingsForEmployee(actorUserID)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	seen := map[string]DirectoryEmployee{}
	if actorUserID != "" {
		seen[actorUserID] = DirectoryEmployee{UserID: actorUserID, DisplayName: actorUserID}
	}
	for _, meeting := range meetings {
		members, err := repo.ListMembers(meeting.ID)
		if err != nil {
			continue
		}
		for _, member := range members {
			if member.UserID == "" {
				continue
			}
			name := strings.TrimSpace(member.DisplayNameSnapshot)
			if name == "" {
				name = member.UserID
			}
			existing, ok := seen[member.UserID]
			if !ok || (existing.DisplayName == existing.UserID && name != member.UserID) {
				seen[member.UserID] = DirectoryEmployee{UserID: member.UserID, DisplayName: name}
			}
		}
	}
	out := make([]DirectoryEmployee, 0, len(seen))
	for _, person := range seen {
		haystack := strings.ToLower(person.UserID + " " + person.DisplayName)
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		out = append(out, person)
	}
	slices.SortFunc(out, func(a, b DirectoryEmployee) int {
		return strings.Compare(a.UserID, b.UserID)
	})
	return out, nil
}
