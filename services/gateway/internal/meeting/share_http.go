package meeting

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/identity"
	"metuai/services/gateway/internal/knowledge"
)

func handleListShares(c *gin.Context, repo Repository) {
	meetingID := c.Param("id")
	current, ok := repo.Get(meetingID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
		return
	}
	if !requireOrganizer(c, repo, current) {
		return
	}
	shares, err := repo.ListShares(meetingID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if shares == nil {
		shares = []MeetingShare{}
	}
	c.JSON(http.StatusOK, gin.H{"readers": shares})
}

func handleAddShare(c *gin.Context, repo Repository, guestVerifier *GuestEmailVerifier) {
	meetingID := c.Param("id")
	current, ok := repo.Get(meetingID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
		return
	}
	if !requireOrganizer(c, repo, current) {
		return
	}
	var body shareBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email required"})
		return
	}
	email := normalizeGuestEmail(body.Email)
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_email"})
		return
	}
	principal := identity.MustPrincipal(c)
	if err := repo.AddShare(meetingID, email, principal.UserID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
		Action:    "share_added",
		Detail:    email,
	})
	shares, _ := repo.ListShares(meetingID)
	payload := gin.H{"ok": true, "readers": shares}
	if guestVerifier != nil {
		if issued, err := guestVerifier.IssueInRoom(current, shareGuestID(email), email); err == nil {
			_ = repo.AppendAudit(AuditEvent{
				MeetingID: meetingID,
				ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
				Action:    "guest_in_room_code_issued",
				Detail:    email + ":share",
			})
			payload["code"] = issued.Code
			payload["magic_url"] = issued.MagicURL
			payload["expires_at"] = issued.ExpiresAt
		}
	}
	c.JSON(http.StatusOK, payload)
}

func handleRemoveShare(c *gin.Context, repo Repository, knowledgeIdx knowledge.Indexer) {
	meetingID := c.Param("id")
	current, ok := repo.Get(meetingID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
		return
	}
	if !requireOrganizer(c, repo, current) {
		return
	}
	email := normalizeGuestEmail(c.Query("email"))
	if email == "" {
		var body shareBody
		_ = c.ShouldBindJSON(&body)
		email = normalizeGuestEmail(body.Email)
	}
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email required"})
		return
	}
	if err := repo.RemoveShare(meetingID, email); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if knowledgeIdx != nil {
		if err := IndexMeetingKnowledge(c.Request.Context(), repo, knowledgeIdx, meetingID, nil); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "share_acl_index_update_failed"})
			return
		}
	}
	principal := identity.MustPrincipal(c)
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
		Action:    "share_removed",
		Detail:    email,
	})
	shares, _ := repo.ListShares(meetingID)
	c.JSON(http.StatusOK, gin.H{"ok": true, "readers": shares})
}

func handleShareVerify(c *gin.Context, repo Repository, guestVerifier *GuestEmailVerifier) {
	meetingID := c.Param("id")
	current, ok := repo.Get(meetingID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
		return
	}
	var body shareBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email required"})
		return
	}
	email := normalizeGuestEmail(body.Email)
	if !repo.HasShare(meetingID, email) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_shared"})
		return
	}
	if guestVerifier == nil {
		writeGuestVerificationRequestError(c, ErrGuestEmailVerificationUnavailable)
		return
	}
	expiresAt, err := guestVerifier.Request(c.Request.Context(), current, shareGuestID(email), email)
	if err != nil {
		writeGuestVerificationRequestError(c, err)
		return
	}
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  "share:" + email,
		Action:    "share_email_verification_requested",
		Detail:    email,
	})
	c.JSON(http.StatusOK, gin.H{"ok": true, "expires_at": expiresAt})
}

func handleShareConfirm(c *gin.Context, repo Repository, guestVerifier *GuestEmailVerifier, guestSecret []byte, knowledgeIdx knowledge.Indexer) {
	meetingID := c.Param("id")
	if _, ok := repo.Get(meetingID); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
		return
	}
	var body shareBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email and code required"})
		return
	}
	email := normalizeGuestEmail(body.Email)
	if !repo.HasShare(meetingID, email) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_shared"})
		return
	}
	guestID := shareGuestID(email)
	if guestVerifier == nil {
		writeGuestVerificationConfirmError(c, ErrGuestEmailVerificationUnavailable)
		return
	}
	challenge, err := guestVerifier.Confirm(meetingID, guestID, email, body.Code)
	if err != nil {
		writeGuestVerificationConfirmError(c, err)
		return
	}
	accessToken, err := grantVerifiedGuestAccess(c.Request.Context(), repo, knowledgeIdx, guestSecret, challenge, strings.Split(challenge.Email, "@")[0], guestEmailSourceShared)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  "guest:" + guestID,
		Action:    "share_email_verified",
		Detail:    challenge.Email,
	})
	c.JSON(http.StatusOK, gin.H{"access_token": accessToken, "email": challenge.Email})
}

func writeGuestVerificationRequestError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrGuestEmailVerificationUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "guest_email_verification_unavailable",
			"hint":  "use_in_room_code",
		})
	case errors.Is(err, ErrGuestEmailVerificationInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_email"})
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": "guest_email_delivery_failed"})
	}
}

func writeGuestVerificationConfirmError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrGuestEmailVerificationExpired):
		c.JSON(http.StatusForbidden, gin.H{"error": "guest_email_verification_expired"})
	case errors.Is(err, ErrGuestEmailVerificationAttemptsExceeded):
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "guest_email_verification_attempt_limit"})
	case errors.Is(err, ErrGuestEmailVerificationInvalid):
		c.JSON(http.StatusForbidden, gin.H{"error": "guest_email_verification_invalid"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
