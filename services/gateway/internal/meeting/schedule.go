package meeting

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	MeetingStatusScheduled = "scheduled"
	MeetingStatusLive      = "live"
	MeetingStatusEnded     = "ended"
)

// MeetingStatus 根据结束标记与 starts_at 推导列表/详情用的 status。
func MeetingStatus(m Meeting, now time.Time) string {
	if m.Ended {
		return MeetingStatusEnded
	}
	if m.StartsAt != nil && now.Before(*m.StartsAt) {
		return MeetingStatusScheduled
	}
	return MeetingStatusLive
}

func parseScheduleTimes(startsAtStr, endsAtStr string) (startsAt, endsAt *time.Time, err error) {
	startsAtStr = strings.TrimSpace(startsAtStr)
	endsAtStr = strings.TrimSpace(endsAtStr)

	if startsAtStr != "" {
		t, parseErr := time.Parse(time.RFC3339, startsAtStr)
		if parseErr != nil {
			return nil, nil, fmt.Errorf("invalid starts_at")
		}
		t = t.UTC()
		if !t.After(time.Now().UTC()) {
			return nil, nil, fmt.Errorf("starts_at must be in the future")
		}
		startsAt = &t
	}

	if endsAtStr != "" {
		if startsAt == nil {
			return nil, nil, fmt.Errorf("ends_at requires starts_at")
		}
		t, parseErr := time.Parse(time.RFC3339, endsAtStr)
		if parseErr != nil {
			return nil, nil, fmt.Errorf("invalid ends_at")
		}
		t = t.UTC()
		if !t.After(*startsAt) {
			return nil, nil, fmt.Errorf("ends_at must be after starts_at")
		}
		endsAt = &t
	}

	return startsAt, endsAt, nil
}

func meetingScheduleFields(m Meeting) gin.H {
	now := time.Now().UTC()
	h := gin.H{
		"status": MeetingStatus(m, now),
	}
	if m.StartsAt != nil {
		h["starts_at"] = m.StartsAt
	}
	if m.EndsAt != nil {
		h["ends_at"] = m.EndsAt
	}
	return h
}

func respondMeetingNotStarted(c *gin.Context, meeting Meeting) {
	resp := gin.H{
		"error":   "meeting_not_started",
		"message": "meeting has not started yet",
	}
	if meeting.StartsAt != nil {
		resp["starts_at"] = meeting.StartsAt
	}
	c.JSON(403, resp)
}

// meetingNotStartedYet 在 starts_at 之前为 true；所有角色（含组织者）均不得签发会话/令牌。
func meetingNotStartedYet(meeting Meeting, now time.Time) bool {
	return meeting.StartsAt != nil && now.Before(*meeting.StartsAt)
}
