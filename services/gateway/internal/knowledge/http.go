package knowledge

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/identity"
)

// ElevationFunc 返回员工当前破窗抬权的会议 ID。
type ElevationFunc func(userID string) []string

// SearchAuditFunc 按命中会议写审计（可空）。
type SearchAuditFunc func(meetingIDs []string, actorKey, query string, hitCount int)

// RegisterRoutes 挂载知识检索（员工或嘉宾）；写入由 meeting 管线 / reindex 完成。
func RegisterRoutes(
	r *gin.Engine,
	idx Indexer,
	employeeSecret, guestSecret []byte,
	elevate ElevationFunc,
	auditSearch SearchAuditFunc,
) {
	if idx == nil {
		idx = NewMemoryIndex()
	}

	r.GET("/v1/knowledge/search", identity.AnyMeetingAuth(employeeSecret, guestSecret), func(c *gin.Context) {
		q := strings.TrimSpace(c.Query("q"))
		principal := identity.MustPrincipal(c)
		opts := SearchOpts{}
		viewerUser := ""
		viewerEmail := ""
		if principal.Kind == identity.KindGuest {
			if principal.MeetingID == "" {
				c.JSON(http.StatusForbidden, gin.H{"error": "guest_meeting_required"})
				return
			}
			if principal.Email == "" {
				c.JSON(http.StatusForbidden, gin.H{"error": "guest_email_verification_required"})
				return
			}
			viewerEmail = principal.Email
			opts.AllowedMeetingIDs = []string{principal.MeetingID}
		} else {
			viewerUser = principal.UserID
			if elevate != nil {
				opts.ElevatedMeetingIDs = elevate(principal.UserID)
			}
		}
		hits, err := idx.Search(c.Request.Context(), q, viewerUser, viewerEmail, opts)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if auditSearch != nil {
			seen := map[string]struct{}{}
			ids := make([]string, 0)
			for _, h := range hits {
				if _, ok := seen[h.Document.MeetingID]; ok {
					continue
				}
				seen[h.Document.MeetingID] = struct{}{}
				ids = append(ids, h.Document.MeetingID)
			}
			actor := principal.Kind + ":" + principal.UserID
			if principal.Kind == identity.KindGuest {
				actor = principal.Kind + ":" + principal.GuestID
			}
			auditSearch(ids, actor, q, len(hits))
		}
		c.JSON(http.StatusOK, gin.H{
			"query":   q,
			"backend": BackendName(idx),
			"hits":    hits,
		})
	})
}
