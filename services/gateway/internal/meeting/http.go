package meeting

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/identity"
	"metuai/services/gateway/internal/knowledge"
	lktoken "metuai/services/gateway/internal/livekit"
)

// browserLiveKitURL 给浏览器用的 LiveKit 地址。
// LIVEKIT_PUBLIC_URL 优先；未设时回退到 RegisterRoutes 传入的服务端 URL（通常即 LIVEKIT_URL）。
// 不改 RegisterRoutes 签名：踢人等仍用传入的 livekitURL（compose 内为 ws://livekit:7880）。
func browserLiveKitURL(serverURL string) string {
	if v := strings.TrimSpace(os.Getenv("LIVEKIT_PUBLIC_URL")); v != "" {
		return v
	}
	return serverURL
}

const clientHeader = "X-Metuai-Client"

// guestSessionTTL 必须覆盖整场会议：嘉宾令牌只在入会时签发一次，前端不做续签。
// 短于会议时长的话，令牌过期后嘉宾的心跳会静默失败，空闲巡检就会把仍在开的会误判为无人并结束。
// 与 LiveKit 入会令牌的有效期（livekit.IssueRoomToken）保持一致。
const guestSessionTTL = 2 * time.Hour

const verifiedGuestSessionTTL = 30 * 24 * time.Hour

type createBody struct {
	Title          string   `json:"title"`
	EmployeeIDs    []string `json:"employee_ids"`
	CoOrganizerIDs []string `json:"co_organizer_ids"`
}

type guestBody struct {
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type guestEmailVerificationBody struct {
	Email   string `json:"email"`
	Code    string `json:"code"`
	Token   string `json:"token"`
	GuestID string `json:"guest_id"`
}

type recordingAckBody struct {
	Password string `json:"password"`
}

type kickBody struct {
	Identity string `json:"identity"`
}

type livekitTokenBody struct {
	DeviceID string `json:"device_id"`
}

type shareBody struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

type chatBody struct {
	Body string `json:"body"`
}

func RegisterRoutes(
	r *gin.Engine,
	repo Repository,
	employeeSecret, guestSecret []byte,
	livekitURL, livekitKey, livekitSecret string,
	allowEmployeeWeb bool,
	s3Bucket string,
	egressRT *EgressRuntime,
	knowledgeIdx knowledge.Indexer,
	breakGlass BreakGlass,
	guestVerifier *GuestEmailVerifier,
	mediaSigner MediaURLSigner,
) {
	registerPipelineTaskRoutes(r, repo, employeeSecret)

	r.POST("/v1/meetings", identity.EmployeeAuth(employeeSecret), func(c *gin.Context) {
		var body createBody
		_ = c.ShouldBindJSON(&body)
		principal := identity.MustPrincipal(c)
		meeting, password, err := repo.Create(body.Title, principal.UserID, "")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if err := repo.AddMembers(meeting.ID, membersFromCreate(body, principal)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meeting.ID,
			ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "meeting_created",
			Detail:    meeting.Title,
		})
		c.JSON(http.StatusOK, gin.H{
			"id":           meeting.ID,
			"title":        meeting.Title,
			"password":     password,
			"organizer_id": meeting.OrganizerID,
			"locked":       meeting.Locked,
			"ended":        meeting.Ended,
		})
	})

	r.GET("/v1/meetings", identity.EmployeeAuth(employeeSecret), func(c *gin.Context) {
		principal := identity.MustPrincipal(c)
		meetings, err := repo.ListMeetingsForEmployee(principal.UserID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items := make([]gin.H, 0, len(meetings))
		for _, meeting := range meetings {
			items = append(items, gin.H{
				"id":             meeting.ID,
				"title":          meeting.Title,
				"organizer_id":   meeting.OrganizerID,
				"locked":         meeting.Locked,
				"ended":          meeting.Ended,
				"pipeline_stage": meeting.PipelineStage,
				"created_at":     meeting.CreatedAt,
			})
		}
		c.JSON(http.StatusOK, gin.H{"meetings": items})
	})

	r.GET("/v1/directory/employees", identity.EmployeeAuth(employeeSecret), func(c *gin.Context) {
		principal := identity.MustPrincipal(c)
		people, err := ListDirectoryEmployees(repo, principal.UserID, c.Query("q"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"employees": people})
	})

	r.POST("/v1/session/login", identity.EmployeeAuth(employeeSecret), func(c *gin.Context) {
		principal := identity.MustPrincipal(c)
		_ = repo.AppendAudit(AuditEvent{
			ActorKey: PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:   "employee_login",
			Detail:   principal.Email,
		})
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	r.POST("/v1/session/logout", identity.EmployeeAuth(employeeSecret), func(c *gin.Context) {
		principal := identity.MustPrincipal(c)
		_ = repo.AppendAudit(AuditEvent{
			ActorKey: PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:   "employee_logout",
			Detail:   principal.Email,
		})
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	r.GET("/v1/meetings/:id", identity.AnyMeetingAuth(employeeSecret, guestSecret), func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canAccessMeetingSession(principal, current, repo) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"id":             current.ID,
			"title":          current.Title,
			"organizer_id":   current.OrganizerID,
			"locked":         current.Locked,
			"ended":          current.Ended,
			"pipeline_stage": current.PipelineStage,
		})
	})

	r.POST("/v1/meetings/:id/guest-session", func(c *gin.Context) {
		meetingID := c.Param("id")
		currentMeeting, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if currentMeeting.Ended {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_ended"})
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
		}, guestSecret, guestSessionTTL)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		_ = repo.UpsertGuestPresence(meetingID, guestID, body.DisplayName)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  "guest:" + guestID,
			Action:    "guest_session_issued",
			Detail:    body.DisplayName,
		})
		c.JSON(http.StatusOK, gin.H{"token": token, "guest_id": guestID})
	})

	auth := identity.AnyMeetingAuth(employeeSecret, guestSecret)
	employeeAuth := identity.EmployeeAuth(employeeSecret)

	r.POST("/v1/meetings/:id/guest-email-verification", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if principal.Kind != identity.KindGuest || principal.MeetingID != meetingID {
			c.JSON(http.StatusForbidden, gin.H{"error": "guest_session_required"})
			return
		}
		if !repo.HasAck(meetingID, PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)) {
			c.JSON(http.StatusForbidden, gin.H{"error": "recording_ack_required"})
			return
		}
		var body guestEmailVerificationBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "email required"})
			return
		}
		expiresAt, err := guestVerifier.Request(c.Request.Context(), current, principal.GuestID, body.Email)
		if err != nil {
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
			return
		}
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "guest_email_verification_requested",
			Detail:    normalizeGuestEmail(body.Email),
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "expires_at": expiresAt})
	})

	r.POST("/v1/meetings/:id/guest-email-verification/confirm", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		_, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if principal.Kind != identity.KindGuest || principal.MeetingID != meetingID {
			c.JSON(http.StatusForbidden, gin.H{"error": "guest_session_required"})
			return
		}
		if !repo.HasAck(meetingID, PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)) {
			c.JSON(http.StatusForbidden, gin.H{"error": "recording_ack_required"})
			return
		}
		var body guestEmailVerificationBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "email and code required"})
			return
		}
		challenge, err := guestVerifier.Confirm(meetingID, principal.GuestID, body.Email, body.Code)
		if err != nil {
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
			return
		}
		accessToken, err := grantVerifiedGuestAccess(c.Request.Context(), repo, knowledgeIdx, guestSecret, challenge, principal.DisplayName, guestEmailSourceParticipant)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "guest_email_verified",
			Detail:    challenge.Email,
		})
		c.JSON(http.StatusOK, gin.H{"access_token": accessToken, "email": challenge.Email})
	})

	r.POST("/v1/meetings/:id/guest-email-verification/magic", func(c *gin.Context) {
		meetingID := c.Param("id")
		if _, ok := repo.Get(meetingID); !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		var body guestEmailVerificationBody
		if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Token) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "token required"})
			return
		}
		if guestVerifier == nil {
			writeGuestVerificationConfirmError(c, ErrGuestEmailVerificationUnavailable)
			return
		}
		challenge, err := guestVerifier.ConfirmMagic(meetingID, body.Token)
		if err != nil {
			writeGuestVerificationConfirmError(c, err)
			return
		}
		source := guestEmailSourceParticipant
		if strings.HasPrefix(challenge.GuestID, "share:") {
			source = guestEmailSourceShared
		}
		accessToken, err := grantVerifiedGuestAccess(c.Request.Context(), repo, knowledgeIdx, guestSecret, challenge, "", source)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  "guest:" + challenge.GuestID,
			Action:    "guest_email_verified",
			Detail:    challenge.Email + ":magic",
		})
		c.JSON(http.StatusOK, gin.H{"access_token": accessToken, "email": challenge.Email})
	})

	r.POST("/v1/meetings/:id/guest-email-verification/in-room", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if !requireOrganizer(c, repo, current) {
			return
		}
		if guestVerifier == nil {
			writeGuestVerificationRequestError(c, ErrGuestEmailVerificationUnavailable)
			return
		}
		var body guestEmailVerificationBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "email required"})
			return
		}
		guestID := normalizeIssuedGuestID(body.GuestID)
		email := normalizeGuestEmail(body.Email)
		if guestID == "" && email != "" && repo.HasShare(meetingID, email) {
			guestID = shareGuestID(email)
		}
		issued, err := guestVerifier.IssueInRoom(current, guestID, email)
		if err != nil {
			writeGuestVerificationRequestError(c, err)
			return
		}
		principal := identity.MustPrincipal(c)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "guest_in_room_code_issued",
			Detail:    issued.Email,
		})
		c.JSON(http.StatusOK, gin.H{
			"ok":         true,
			"email":      issued.Email,
			"guest_id":   issued.GuestID,
			"code":       issued.Code,
			"magic_url":  issued.MagicURL,
			"expires_at": issued.ExpiresAt,
		})
	})

	r.GET("/v1/meetings/:id/guest-participants", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if !requireOrganizer(c, repo, current) {
			return
		}
		guests, err := repo.ListGuestParticipants(meetingID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		ids := make([]string, 0, len(guests))
		for _, guest := range guests {
			ids = append(ids, guest.GuestID)
		}
		c.JSON(http.StatusOK, gin.H{"guests": guests, "guest_ids": ids})
	})

	r.POST("/v1/meetings/:id/recording-ack", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if current.Ended {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_ended"})
			return
		}
		principal := identity.MustPrincipal(c)
		if principal.Kind == identity.KindGuest && principal.MeetingID != meetingID {
			c.JSON(http.StatusForbidden, gin.H{"error": "wrong_meeting"})
			return
		}
		principalKey := PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		if current.Locked && !repo.HasAck(meetingID, principalKey) {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_locked"})
			return
		}
		if principal.Kind == identity.KindEmployee && principal.UserID != current.OrganizerID && !repo.IsInvitedEmployee(meetingID, principal.UserID) && !repo.HasAck(meetingID, principalKey) {
			var body recordingAckBody
			_ = c.ShouldBindJSON(&body)
			if body.Password == "" || !repo.CheckPassword(meetingID, body.Password) {
				c.JSON(http.StatusForbidden, gin.H{"error": "meeting_password_required"})
				return
			}
		}
		if err := repo.AckRecording(meetingID, principalKey); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if principal.Kind == identity.KindEmployee {
			if err := repo.MarkMemberJoined(meetingID, principal.UserID, principal.DisplayName); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
		if principal.Kind == identity.KindGuest {
			_ = repo.UpsertGuestPresence(meetingID, principal.GuestID, principal.DisplayName)
		}
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  principalKey,
			Action:    "recording_ack",
		})
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	r.POST("/v1/meetings/:id/livekit-token", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		currentMeeting, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if currentMeeting.Ended {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_ended"})
			return
		}
		principal := identity.MustPrincipal(c)
		if principal.Kind == identity.KindGuest && principal.MeetingID != meetingID {
			c.JSON(http.StatusForbidden, gin.H{"error": "wrong_meeting"})
			return
		}
		if currentMeeting.Locked {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_locked"})
			return
		}

		// 生产策略：员工必须走 Tauri。PoC 可用 DEV_ALLOW_EMPLOYEE_WEB=true 放开。
		if principal.Kind == identity.KindEmployee && !allowEmployeeWeb {
			if !strings.EqualFold(c.GetHeader(clientHeader), "tauri") {
				_ = repo.AppendAudit(AuditEvent{
					MeetingID: meetingID,
					ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
					Action:    "employee_web_blocked",
					Detail:    c.GetHeader("User-Agent"),
				})
				c.JSON(http.StatusForbidden, gin.H{"error": "employee_web_forbidden"})
				return
			}
		}

		identityID := principal.UserID
		if principal.Kind == identity.KindGuest {
			identityID = "guest:" + principal.GuestID
		}
		if repo.IsKicked(meetingID, identityID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "kicked"})
			return
		}

		var tokenBody livekitTokenBody
		_ = c.ShouldBindJSON(&tokenBody)
		identityID = lktoken.DeviceIdentity(identityID, tokenBody.DeviceID)

		principalKey := PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		if !repo.HasAck(meetingID, principalKey) {
			c.JSON(http.StatusForbidden, gin.H{"error": "recording_ack_required"})
			return
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
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  principalKey,
			Action:    "livekit_token_issued",
		})
		_ = repo.TouchActivity(meetingID)
		// 录制在会中开始：空房 deferred 后由心跳/下次令牌重试；后入会靠 EnsureLateTracks 补轨。
		egressCtx, cancelEgress := detachedContext(c, 15*time.Second)
		egressRT.EnsureStarted(egressCtx, repo, meetingID)
		egressRT.EnsureLateTracks(egressCtx, repo, meetingID)
		cancelEgress()
		c.JSON(http.StatusOK, gin.H{
			"token":        token,
			"livekit_url":  browserLiveKitURL(livekitURL),
			"identity":     identityID,
			"is_organizer": principal.Kind == identity.KindEmployee && isMeetingManager(repo, currentMeeting, principal.UserID),
			"meeting_id":   meetingID,
			"organizer_id": currentMeeting.OrganizerID,
		})
	})

	r.POST("/v1/meetings/:id/heartbeat", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if current.Ended {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_ended"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canAccessMeetingSession(principal, current, repo) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		if err := repo.TouchActivity(meetingID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// 心跳时人多半已经进 LiveKit 房：补开「拿令牌时房还是空的」那次 deferred 录制。
		egressCtx, cancel := detachedContext(c, 15*time.Second)
		defer cancel()
		egressRT.EnsureStarted(egressCtx, repo, meetingID)
		egressRT.EnsureLateTracks(egressCtx, repo, meetingID)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	r.POST("/v1/meetings/:id/lock", employeeAuth, func(c *gin.Context) {
		handleOrganizerLock(c, repo, true)
	})
	r.POST("/v1/meetings/:id/unlock", employeeAuth, func(c *gin.Context) {
		handleOrganizerLock(c, repo, false)
	})

	r.POST("/v1/meetings/:id/end", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if !requireOrganizer(c, repo, current) {
			return
		}
		if err := repo.End(meetingID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// 收尾服务端录制：停止 Egress 并按真实终态更新媒体状态。
		// Egress 从未启动时退回 pending 计划，绝不静默标 ready。
		egressCtx, cancelEgress := detachedContext(c, 60*time.Second)
		egressRT.FinalizeOrPlan(egressCtx, repo, meetingID, s3Bucket, "")
		cancelEgress()

		principal := identity.MustPrincipal(c)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "meeting_ended",
		})
		EnqueueAfterEnd(repo, meetingID)
		artifacts, _ := repo.ListMediaArtifacts(meetingID)
		c.JSON(http.StatusOK, gin.H{"ok": true, "ended": true, "artifacts": artifacts})
	})

	r.POST("/v1/meetings/:id/leave", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canAccessMeetingSession(principal, current, repo) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		actor := PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  actor,
			Action:    "meeting_left",
		})
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	r.POST("/v1/meetings/:id/reset-password", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if current.Ended {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_ended"})
			return
		}
		if !requireOrganizer(c, repo, current) {
			return
		}
		plain, err := repo.ResetPassword(meetingID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		principal := identity.MustPrincipal(c)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "password_reset",
		})
		c.JSON(http.StatusOK, gin.H{"password": plain})
	})

	r.POST("/v1/meetings/:id/kick", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if current.Ended {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_ended"})
			return
		}
		if !requireOrganizer(c, repo, current) {
			return
		}
		var body kickBody
		if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Identity) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "identity required"})
			return
		}
		identityID := strings.TrimSpace(body.Identity)
		userKey := lktoken.UserKey(identityID)
		if isMeetingManager(repo, current, userKey) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot_kick_organizer"})
			return
		}
		if err := repo.Kick(meetingID, userKey); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// 尽力从 LiveKit 房间移除该用户所有设备；失败不回滚已落库的踢出状态。
		_ = lktoken.RemoveByUserKey(context.Background(), livekitURL, livekitKey, livekitSecret, meetingID, identityID)
		principal := identity.MustPrincipal(c)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "participant_kicked",
			Detail:    userKey,
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "identity": userKey})
	})

	r.GET("/v1/meetings/:id/shared-readers", employeeAuth, func(c *gin.Context) {
		handleListShares(c, repo)
	})
	r.POST("/v1/meetings/:id/shared-readers", employeeAuth, func(c *gin.Context) {
		handleAddShare(c, repo, guestVerifier)
	})
	r.DELETE("/v1/meetings/:id/shared-readers", employeeAuth, func(c *gin.Context) {
		handleRemoveShare(c, repo, knowledgeIdx)
	})
	r.POST("/v1/meetings/:id/shared-readers/verify", func(c *gin.Context) {
		handleShareVerify(c, repo, guestVerifier)
	})
	r.POST("/v1/meetings/:id/shared-readers/confirm", func(c *gin.Context) {
		handleShareConfirm(c, repo, guestVerifier, guestSecret, knowledgeIdx)
	})

	r.POST("/v1/meetings/:id/chat", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if current.Ended {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_ended"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canAccessMeetingSession(principal, current, repo) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		var body chatBody
		if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Body) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "body required"})
			return
		}
		msg, err := repo.AddChat(ChatMessage{
			MeetingID:   meetingID,
			SenderKey:   PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			DisplayName: principal.DisplayName,
			Body:        strings.TrimSpace(body.Body),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		_ = repo.TouchActivity(meetingID)
		c.JSON(http.StatusOK, msg)
	})

	r.GET("/v1/meetings/:id/chat", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canAccessMeetingSession(principal, current, repo) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		msgs, err := repo.ListChat(meetingID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"messages": msgs})
	})

	r.GET("/v1/meetings/:id/media", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canAccessMeetingArtifacts(principal, current, repo, breakGlass) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		items, err := repo.ListMediaArtifacts(meetingID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		recordArtifactView(repo, meetingID, PrincipalKey(principal.Kind, principal.UserID, principal.GuestID), "media")
		c.JSON(http.StatusOK, gin.H{"artifacts": signMediaURLs(c, items, mediaSigner, s3Bucket)})
	})

	// 员工回传本机录音审计（架构 §5.2）；只接受 local_recording_* 动作，防乱写。
	r.POST("/v1/meetings/:id/local-recording/audit", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canAccessMeetingSession(principal, current, repo) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		var body struct {
			Events []struct {
				Action string `json:"action"`
				Detail string `json:"detail"`
			} `json:"events"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || len(body.Events) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "events required"})
			return
		}
		if len(body.Events) > 200 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "too_many_events"})
			return
		}
		actor := PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		accepted := 0
		for _, ev := range body.Events {
			action := strings.TrimSpace(ev.Action)
			if !strings.HasPrefix(action, "local_recording_") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_action", "action": action})
				return
			}
			detail := ev.Detail
			if len(detail) > 2000 {
				detail = detail[:2000]
			}
			if err := repo.AppendAudit(AuditEvent{
				MeetingID: meetingID,
				ActorKey:  actor,
				Action:    action,
				Detail:    detail,
			}); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			accepted++
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "accepted": accepted})
	})

	r.GET("/v1/meetings/:id/audit", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		if _, ok := repo.Get(meetingID); !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !principal.HasRole("audit_admin") {
			c.JSON(http.StatusForbidden, gin.H{"error": "audit_admin_required"})
			return
		}
		items, err := repo.ListAudit(meetingID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"events": items})
	})

	// 会后假流水线：组织者或员工可触发；Worker 也可带员工 JWT 调用。
	r.POST("/v1/meetings/:id/pipeline/run-fake", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if !current.Ended {
			c.JSON(http.StatusBadRequest, gin.H{"error": "meeting_not_ended"})
			return
		}
		if !requireOrganizer(c, repo, current) {
			return
		}
		stage, err := RunFakePipeline(repo, meetingID, knowledgeIdx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "stage": current.PipelineStage})
			return
		}
		CompleteOpenPipelineTasks(repo, meetingID, PipelineKindFake)
		c.JSON(http.StatusOK, gin.H{"ok": true, "pipeline_stage": stage})
	})

	// 手动把当前纪要/转写写入知识检索副本（组织者）。
	r.POST("/v1/meetings/:id/knowledge/index", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if !requireOrganizer(c, repo, current) {
			return
		}
		if knowledgeIdx == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "knowledge_not_configured"})
			return
		}
		if err := IndexMeetingKnowledge(c.Request.Context(), repo, knowledgeIdx, meetingID, nil); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		principal := identity.MustPrincipal(c)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "knowledge_reindex",
			Detail:    knowledge.BackendName(knowledgeIdx),
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "backend": knowledge.BackendName(knowledgeIdx)})
	})

	// Worker 提交 ASR 结果（覆盖既有转写，推进到 TRANSCRIPT_READY；不自动写纪要）。
	r.POST("/v1/meetings/:id/pipeline/asr-result", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if !current.Ended {
			c.JSON(http.StatusBadRequest, gin.H{"error": "meeting_not_ended"})
			return
		}
		if !requireOrganizer(c, repo, current) {
			return
		}
		var body struct {
			Backend  string           `json:"backend"`
			Segments []ASRResultInput `json:"segments"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
			return
		}
		principal := identity.MustPrincipal(c)
		actor := PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		stage, err := ApplyASRResult(repo, meetingID, actor, body.Backend, body.Segments)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		CompleteOpenPipelineTasks(repo, meetingID, PipelineKindASR)
		c.JSON(http.StatusOK, gin.H{
			"ok":             true,
			"pipeline_stage": stage,
			"segments":       len(body.Segments),
			"backend":        body.Backend,
		})
	})

	// 会后页一键 stub ASR（不启 Python）；真模型仍走 Worker。
	r.POST("/v1/meetings/:id/pipeline/run-asr-stub", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if !current.Ended {
			c.JSON(http.StatusBadRequest, gin.H{"error": "meeting_not_ended"})
			return
		}
		if !requireOrganizer(c, repo, current) {
			return
		}
		principal := identity.MustPrincipal(c)
		actor := PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		stage, err := RunASRStub(repo, meetingID, actor)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "pipeline_stage": stage, "backend": "stub"})
	})

	r.POST("/v1/meetings/:id/pipeline/manual-review", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if !current.Ended {
			c.JSON(http.StatusBadRequest, gin.H{"error": "meeting_not_ended"})
			return
		}
		if !requireOrganizer(c, repo, current) {
			return
		}
		var body struct {
			Reason string `json:"reason"`
		}
		_ = c.ShouldBindJSON(&body)
		principal := identity.MustPrincipal(c)
		actor := PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		stage, err := MarkManualReview(repo, meetingID, actor, body.Reason)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "pipeline_stage": stage})
	})

	r.GET("/v1/meetings/:id/pipeline", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canAccessMeetingArtifacts(principal, current, repo, breakGlass) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"meeting_id":     current.ID,
			"title":          current.Title,
			"ended":          current.Ended,
			"pipeline_stage": current.PipelineStage,
		})
	})

	r.GET("/v1/meetings/:id/transcript", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canAccessMeetingArtifacts(principal, current, repo, breakGlass) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		segs, err := repo.ListTranscript(meetingID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		recordArtifactView(repo, meetingID, PrincipalKey(principal.Kind, principal.UserID, principal.GuestID), "transcript")
		c.JSON(http.StatusOK, gin.H{"segments": segs})
	})

	r.GET("/v1/meetings/:id/summary", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canAccessMeetingArtifacts(principal, current, repo, breakGlass) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		sum, ok := repo.GetSummary(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "summary_not_ready"})
			return
		}
		recordArtifactView(repo, meetingID, PrincipalKey(principal.Kind, principal.UserID, principal.GuestID), "summary")
		c.JSON(http.StatusOK, sum)
	})

	r.PATCH("/v1/meetings/:id/summary", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canEditMeetingArtifacts(principal, current, repo) {
			c.JSON(http.StatusForbidden, gin.H{"error": "employee_participant_required"})
			return
		}
		var next MeetingSummary
		if err := c.ShouldBindJSON(&next); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
			return
		}
		actor := PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		updated, err := ApplySummaryEdit(repo, meetingID, actor, next)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrSummaryNotReady) {
				status = http.StatusNotFound
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		if knowledgeIdx != nil {
			_ = IndexMeetingKnowledge(c.Request.Context(), repo, knowledgeIdx, meetingID, nil)
		}
		c.JSON(http.StatusOK, updated)
	})

	r.POST("/v1/meetings/:id/summary/action-items/:idx/complete", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canEditMeetingArtifacts(principal, current, repo) {
			c.JSON(http.StatusForbidden, gin.H{"error": "employee_participant_required"})
			return
		}
		idx, err := strconv.Atoi(c.Param("idx"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_index"})
			return
		}
		actor := PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		updated, err := CompleteActionItem(repo, meetingID, actor, idx)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrSummaryNotReady) {
				status = http.StatusNotFound
			}
			if errors.Is(err, ErrActionAlreadyDone) {
				c.JSON(http.StatusOK, updated)
				return
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		if knowledgeIdx != nil {
			_ = IndexMeetingKnowledge(c.Request.Context(), repo, knowledgeIdx, meetingID, nil)
		}
		c.JSON(http.StatusOK, updated)
	})

	r.GET("/v1/meetings/:id/summary/revisions", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canAccessMeetingArtifacts(principal, current, repo, breakGlass) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		items, err := repo.ListSummaryRevisions(meetingID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"revisions": items})
	})

	// 显式下载或导出：与「在线查看」同一套 ACL；下载记 artifact_download，转写+纪要打包记 artifact_export。
	r.POST("/v1/meetings/:id/artifacts/download", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !canAccessMeetingArtifacts(principal, current, repo, breakGlass) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		var body struct {
			Kind       string `json:"kind"` // transcript | summary | media | export
			ArtifactID string `json:"artifact_id"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Kind == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "kind_required"})
			return
		}
		actor := PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		detail := body.Kind
		if body.ArtifactID != "" {
			detail = body.Kind + ":" + body.ArtifactID
		}
		action := "artifact_download"
		if body.Kind == "export" {
			action = "artifact_export"
		}
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  actor,
			Action:    action,
			Detail:    detail,
		})

		switch body.Kind {
		case "transcript":
			segs, err := repo.ListTranscript(meetingID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"kind": "transcript", "segments": segs, "audited": true})
		case "summary":
			sum, ok := repo.GetSummary(meetingID)
			if !ok {
				c.JSON(http.StatusNotFound, gin.H{"error": "summary_not_ready"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"kind": "summary", "summary": sum, "audited": true})
		case "export":
			segs, err := repo.ListTranscript(meetingID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			payload := gin.H{"kind": "export", "segments": segs, "audited": true, "has_summary": false}
			if sum, ok := repo.GetSummary(meetingID); ok {
				payload["summary"] = sum
				payload["has_summary"] = true
			}
			c.JSON(http.StatusOK, payload)
		case "media":
			arts, err := repo.ListMediaArtifacts(meetingID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if body.ArtifactID != "" {
				filtered := make([]MediaArtifact, 0, 1)
				for _, a := range arts {
					if a.ID == body.ArtifactID {
						filtered = append(filtered, a)
						break
					}
				}
				if len(filtered) == 0 {
					c.JSON(http.StatusNotFound, gin.H{"error": "artifact_not_found"})
					return
				}
				arts = filtered
			}
			items := signMediaURLs(c, arts, mediaSigner, s3Bucket)
			c.JSON(http.StatusOK, gin.H{"kind": "media", "artifacts": items, "audited": true})
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "unknown_kind"})
		}
	})

	// 破窗：非组织者员工申请；另一员工批准后时限内可检索/下载。
	r.POST("/v1/meetings/:id/break-glass", employeeAuth, func(c *gin.Context) {
		if breakGlass == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "break_glass_not_configured"})
			return
		}
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !principal.HasRole("audit_admin") {
			c.JSON(http.StatusForbidden, gin.H{"error": "audit_admin_required"})
			return
		}
		if principal.UserID == current.OrganizerID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "organizer_needs_no_break_glass"})
			return
		}
		var body struct {
			Reason string `json:"reason"`
		}
		_ = c.ShouldBindJSON(&body)
		req, err := breakGlass.Apply(meetingID, principal.UserID, body.Reason)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "break_glass_apply",
			Detail:    req.ID + ":" + req.Reason,
		})
		c.JSON(http.StatusOK, req)
	})

	r.POST("/v1/meetings/:id/break-glass/:reqId/approve", employeeAuth, func(c *gin.Context) {
		if breakGlass == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "break_glass_not_configured"})
			return
		}
		meetingID := c.Param("id")
		if _, ok := repo.Get(meetingID); !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !principal.HasRole("audit_admin") && !principal.HasRole("system_admin") {
			c.JSON(http.StatusForbidden, gin.H{"error": "approver_role_required"})
			return
		}
		req, err := breakGlass.Approve(c.Param("reqId"), principal.UserID, time.Hour)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.MeetingID != meetingID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "meeting_mismatch"})
			return
		}
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "break_glass_approve",
			Detail:    req.ID + ":applicant=" + req.Applicant,
		})
		c.JSON(http.StatusOK, req)
	})

	r.POST("/v1/meetings/:id/break-glass/:reqId/deny", employeeAuth, func(c *gin.Context) {
		if breakGlass == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "break_glass_not_configured"})
			return
		}
		meetingID := c.Param("id")
		if _, ok := repo.Get(meetingID); !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !principal.HasRole("audit_admin") && !principal.HasRole("system_admin") {
			c.JSON(http.StatusForbidden, gin.H{"error": "approver_role_required"})
			return
		}
		req, err := breakGlass.Deny(c.Param("reqId"), principal.UserID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.MeetingID != meetingID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "meeting_mismatch"})
			return
		}
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "break_glass_deny",
			Detail:    req.ID + ":applicant=" + req.Applicant,
		})
		c.JSON(http.StatusOK, req)
	})

	r.POST("/v1/meetings/:id/break-glass/:reqId/revoke", employeeAuth, func(c *gin.Context) {
		if breakGlass == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "break_glass_not_configured"})
			return
		}
		meetingID := c.Param("id")
		if _, ok := repo.Get(meetingID); !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !principal.HasRole("audit_admin") && !principal.HasRole("system_admin") {
			c.JSON(http.StatusForbidden, gin.H{"error": "approver_role_required"})
			return
		}
		req, err := breakGlass.Revoke(c.Param("reqId"), principal.UserID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.MeetingID != meetingID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "meeting_mismatch"})
			return
		}
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "break_glass_revoke",
			Detail:    req.ID + ":applicant=" + req.Applicant,
		})
		c.JSON(http.StatusOK, req)
	})

	r.GET("/v1/meetings/:id/break-glass", employeeAuth, func(c *gin.Context) {
		principal := identity.MustPrincipal(c)
		if !principal.HasRole("audit_admin") && !principal.HasRole("system_admin") {
			c.JSON(http.StatusForbidden, gin.H{"error": "admin_role_required"})
			return
		}
		if breakGlass == nil {
			c.JSON(http.StatusOK, gin.H{"requests": []BreakGlassRequest{}})
			return
		}
		meetingID := c.Param("id")
		if _, ok := repo.Get(meetingID); !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"requests": breakGlass.ListForMeeting(meetingID)})
	})

	r.GET("/v1/retention", employeeAuth, func(c *gin.Context) {
		principal := identity.MustPrincipal(c)
		if !principal.HasRole("system_admin") {
			c.JSON(http.StatusForbidden, gin.H{"error": "system_admin_required"})
			return
		}
		policy, err := repo.GetRetentionPolicy()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, policy)
	})

	r.PUT("/v1/retention", employeeAuth, func(c *gin.Context) {
		principal := identity.MustPrincipal(c)
		if !principal.HasRole("system_admin") {
			c.JSON(http.StatusForbidden, gin.H{"error": "system_admin_required"})
			return
		}
		var body RetentionPolicy
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
			return
		}
		body.UpdatedBy = principal.UserID
		if err := repo.SetRetentionPolicy(body); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		policy, _ := repo.GetRetentionPolicy()
		_ = repo.AppendAudit(AuditEvent{
			ActorKey: PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:   "retention_policy_updated",
			Detail:   fmt.Sprintf("media=%d video=%d knowledge=%d", policy.MediaTTLSeconds, policy.VideoTTLSeconds, policy.KnowledgeTTLSeconds),
		})
		c.JSON(http.StatusOK, policy)
	})
}

func canAccessMeetingSession(principal identity.Principal, current Meeting, repo Repository) bool {
	if principal.Kind == identity.KindGuest {
		return principal.MeetingID == current.ID && repo.HasAck(current.ID, PrincipalKey(principal.Kind, principal.UserID, principal.GuestID))
	}
	if principal.Kind != identity.KindEmployee {
		return false
	}
	return isMeetingManager(repo, current, principal.UserID) ||
		repo.HasAck(current.ID, PrincipalKey(principal.Kind, principal.UserID, principal.GuestID))
}

// canAccessMeetingArtifacts：组织者、已录音确认的员工参会人、已验证嘉宾、或有效破窗申请人。
func canAccessMeetingArtifacts(principal identity.Principal, current Meeting, repo Repository, bg BreakGlass) bool {
	if principal.Kind == identity.KindGuest {
		if principal.MeetingID != current.ID || principal.Email == "" || !repo.HasAck(current.ID, PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)) {
			return false
		}
		for _, email := range mustGuestEmails(repo, current.ID) {
			if strings.EqualFold(email, principal.Email) {
				return true
			}
		}
		return false
	}
	if principal.Kind != identity.KindEmployee {
		return false
	}
	if isMeetingManager(repo, current, principal.UserID) {
		return true
	}
	if bg != nil && bg.HasActiveGrant(current.ID, principal.UserID) {
		return true
	}
	if repo != nil {
		if parts, err := repo.ListEmployeeParticipantIDs(current.ID); err == nil {
			for _, uid := range parts {
				if uid == principal.UserID {
					return true
				}
			}
		}
	}
	return false
}

func canEditMeetingArtifacts(principal identity.Principal, current Meeting, repo Repository) bool {
	if principal.Kind != identity.KindEmployee {
		return false
	}
	if isMeetingManager(repo, current, principal.UserID) {
		return true
	}
	if repo.HasAck(current.ID, PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)) {
		return true
	}
	if parts, err := repo.ListEmployeeParticipantIDs(current.ID); err == nil {
		for _, uid := range parts {
			if uid == principal.UserID {
				return true
			}
		}
	}
	return false
}

func signMediaURLs(c *gin.Context, arts []MediaArtifact, mediaSigner MediaURLSigner, s3Bucket string) []MediaArtifact {
	out := make([]MediaArtifact, 0, len(arts))
	for _, a := range arts {
		if mediaSigner != nil && a.ObjectKey != "" && a.Status == "ready" {
			key := stripBucketPrefix(a.ObjectKey, s3Bucket)
			if url, err := mediaSigner.SignGetURL(c.Request.Context(), key, 15*time.Minute); err == nil {
				a.DownloadURL = url
			}
		}
		out = append(out, a)
	}
	return out
}

func stripBucketPrefix(objectKey, bucket string) string {
	objectKey = strings.TrimSpace(objectKey)
	bucket = strings.TrimSpace(strings.Trim(bucket, "/"))
	if bucket == "" {
		return objectKey
	}
	if after, ok := strings.CutPrefix(objectKey, bucket+"/"); ok {
		return after
	}
	return objectKey
}

func mustGuestEmails(repo Repository, meetingID string) []string {
	if repo == nil {
		return nil
	}
	emails, err := repo.ListGuestEmails(meetingID)
	if err != nil {
		return nil
	}
	return emails
}

// detachedContext 保留请求上下文的值，但不随客户端断开而取消。
// 结束会议会触发 StopEgress + 轮询终态，浏览器一跳转就中断的话，
// 媒体状态会永远停在 started。
func detachedContext(c *gin.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(c.Request.Context()), timeout)
}

func isMeetingManager(repo Repository, current Meeting, userID string) bool {
	return userID == current.OrganizerID || repo.IsOrganizerOrCoOrganizer(current.ID, userID)
}

func requireOrganizer(c *gin.Context, repo Repository, current Meeting) bool {
	principal := identity.MustPrincipal(c)
	if principal.Kind != identity.KindEmployee || !isMeetingManager(repo, current, principal.UserID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "organizer_required"})
		return false
	}
	return true
}

func handleOrganizerLock(c *gin.Context, repo Repository, locked bool) {
	meetingID := c.Param("id")
	current, ok := repo.Get(meetingID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
		return
	}
	if current.Ended {
		c.JSON(http.StatusForbidden, gin.H{"error": "meeting_ended"})
		return
	}
	if !requireOrganizer(c, repo, current) {
		return
	}
	if err := repo.SetLocked(meetingID, locked); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	action := "meeting_unlocked"
	if locked {
		action = "meeting_locked"
	}
	principal := identity.MustPrincipal(c)
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
		Action:    action,
	})
	c.JSON(http.StatusOK, gin.H{"ok": true, "locked": locked})
}

func membersFromCreate(body createBody, principal identity.Principal) []MeetingMember {
	roles := make(map[string]MeetingMemberRole, len(body.EmployeeIDs)+len(body.CoOrganizerIDs)+1)
	add := func(userID string, role MeetingMemberRole) {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			return
		}
		if existing, ok := roles[userID]; !ok || memberRolePriority(role) > memberRolePriority(existing) {
			roles[userID] = role
		}
	}
	add(principal.UserID, MeetingMemberOrganizer)
	for _, userID := range body.EmployeeIDs {
		add(userID, MeetingMemberInvited)
	}
	// 共同组织者自动加入内部邀请，无需前端重复提交两组 ID。
	for _, userID := range body.CoOrganizerIDs {
		add(userID, MeetingMemberCoOrganizer)
	}
	members := make([]MeetingMember, 0, len(roles))
	for userID, role := range roles {
		displayName := ""
		if userID == principal.UserID {
			displayName = principal.DisplayName
		}
		members = append(members, MeetingMember{
			UserID:              userID,
			Role:                role,
			DisplayNameSnapshot: displayName,
		})
	}
	return members
}

func recordArtifactView(repo Repository, meetingID, actor, resource string) {
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  actor,
		Action:    "artifact_view",
		Detail:    resource,
	})
}
