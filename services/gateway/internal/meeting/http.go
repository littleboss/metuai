package meeting

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/identity"
	lktoken "metuai/services/gateway/internal/livekit"
)

type createBody struct {
	Title string `json:"title"`
}

type guestBody struct {
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

func RegisterRoutes(
	r *gin.Engine,
	repo Repository,
	employeeSecret, guestSecret []byte,
	livekitURL, livekitKey, livekitSecret string,
	allowEmployeeWeb bool,
) {
	_ = allowEmployeeWeb

	r.POST("/v1/meetings", identity.EmployeeAuth(employeeSecret), func(c *gin.Context) {
		var body createBody
		_ = c.ShouldBindJSON(&body)
		principal := identity.MustPrincipal(c)
		meeting, password, err := repo.Create(body.Title, principal.UserID, "")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"id":           meeting.ID,
			"title":        meeting.Title,
			"password":     password,
			"organizer_id": meeting.OrganizerID,
		})
	})

	r.POST("/v1/meetings/:id/guest-session", func(c *gin.Context) {
		meetingID := c.Param("id")
		currentMeeting, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if currentMeeting.Locked {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_locked"})
			return
		}

		var body guestBody
		if err := c.ShouldBindJSON(&body); err != nil || body.Password == "" || body.DisplayName == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "password and display_name required"})
			return
		}
		if !repo.CheckPassword(meetingID, body.Password) {
			c.JSON(http.StatusForbidden, gin.H{"error": "invalid_password"})
			return
		}

		guestID, err := RandomID("gst_")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		token, err := identity.IssueGuestSession(identity.Principal{
			Kind:        identity.KindGuest,
			GuestID:     guestID,
			MeetingID:   meetingID,
			DisplayName: body.DisplayName,
		}, guestSecret, 15*time.Minute)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"token": token, "guest_id": guestID})
	})

	auth := identity.AnyMeetingAuth(employeeSecret, guestSecret)

	r.POST("/v1/meetings/:id/recording-ack", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		if _, ok := repo.Get(meetingID); !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if principal.Kind == identity.KindGuest && principal.MeetingID != meetingID {
			c.JSON(http.StatusForbidden, gin.H{"error": "wrong_meeting"})
			return
		}
		principalKey := PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		if err := repo.AckRecording(meetingID, principalKey); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	r.POST("/v1/meetings/:id/livekit-token", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		currentMeeting, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if principal.Kind == identity.KindGuest && principal.MeetingID != meetingID {
			c.JSON(http.StatusForbidden, gin.H{"error": "wrong_meeting"})
			return
		}
		if currentMeeting.Locked && principal.UserID != currentMeeting.OrganizerID {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_locked"})
			return
		}

		principalKey := PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		if !repo.HasAck(meetingID, principalKey) {
			c.JSON(http.StatusForbidden, gin.H{"error": "recording_ack_required"})
			return
		}

		identityID := principal.UserID
		if principal.Kind == identity.KindGuest {
			identityID = "guest:" + principal.GuestID
		}
		token, err := lktoken.IssueRoomToken(
			livekitKey,
			livekitSecret,
			meetingID,
			identityID,
			principal.DisplayName,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"token": token, "livekit_url": livekitURL})
	})
}
