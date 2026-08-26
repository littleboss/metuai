package upload

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/identity"
	"metuai/services/gateway/internal/meeting"
)

// RegisterRoutes 仅员工可上传本机麦克风备份分块。
// blobs 可为 nil / 未 Enabled：complete 仍合并本地 spool 并写 ready（不碰 MinIO）。
func RegisterRoutes(r *gin.Engine, store *Store, meetings meeting.Repository, employeeSecret []byte, blobs BlobStore, s3Bucket string) {
	auth := identity.EmployeeAuth(employeeSecret)
	requireParticipant := func(c *gin.Context, meetingID string) (identity.Principal, bool) {
		principal := identity.MustPrincipal(c)
		current, ok := meetings.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return identity.Principal{}, false
		}
		principalKey := meeting.PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		if principal.UserID != current.OrganizerID && !meetings.HasAck(meetingID, principalKey) {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_participant_required"})
			return identity.Principal{}, false
		}
		return principal, true
	}

	r.GET("/v1/meetings/:id/local-recording/:uploadId/status", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		principal, ok := requireParticipant(c, meetingID)
		if !ok {
			return
		}
		meta := ChunkMeta{
			MeetingID: meetingID,
			UserID:    principal.UserID,
			UploadID:  c.Param("uploadId"),
		}
		st, err := store.Status(meta)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"received":   st.Received,
			"finalized":  st.Finalized,
			"checksum":   st.Checksum,
			"object_key": store.QualifiedObjectKey(s3Bucket, meta),
		})
	})

	r.PUT("/v1/meetings/:id/local-recording/:uploadId/chunks/:index", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		principal, ok := requireParticipant(c, meetingID)
		if !ok {
			return
		}
		index, err := strconv.Atoi(c.Param("index"))
		if err != nil || index < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_index"})
			return
		}
		checksum := c.GetHeader("X-Checksum-Sha256")
		if checksum == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "checksum_required"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, 8<<20)) // 8 MiB / chunk
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "read_body"})
			return
		}
		meta := ChunkMeta{
			MeetingID: meetingID,
			UserID:    principal.UserID,
			UploadID:  c.Param("uploadId"),
			Index:     index,
			Checksum:  checksum,
		}
		if err := store.PutChunk(meta, bytes.NewReader(body)); err != nil {
			if err.Error() == "checksum_mismatch" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "checksum_mismatch"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		_ = meetings.AppendAudit(meeting.AuditEvent{
			MeetingID: meetingID,
			ActorKey:  meeting.PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "local_recording_chunk",
			Detail:    meta.UploadID + "#" + strconv.Itoa(index),
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "index": index})
	})

	r.POST("/v1/meetings/:id/local-recording/:uploadId/complete", auth, func(c *gin.Context) {
		meetingID := c.Param("id")
		principal, ok := requireParticipant(c, meetingID)
		if !ok {
			return
		}
		var body struct {
			Parts int `json:"parts"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Parts <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "parts required"})
			return
		}
		meta := ChunkMeta{
			MeetingID: meetingID,
			UserID:    principal.UserID,
			UploadID:  c.Param("uploadId"),
		}
		final, err := store.Finalize(meta, body.Parts)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		actor := meeting.PrincipalKey(principal.Kind, principal.UserID, principal.GuestID)
		_ = meetings.AppendAudit(meeting.AuditEvent{
			MeetingID: meetingID,
			ActorKey:  actor,
			Action:    "local_recording_uploaded",
			Detail:    final,
		})

		objectKey := store.ObjectKey(meta)
		qualified := store.QualifiedObjectKey(s3Bucket, meta)
		status := "ready"
		detail := "checksum=" + final + "; user=" + principal.UserID + "; path=" + store.MergedPath(meta)
		storedIn := "local_spool"

		if blobs != nil && blobs.Enabled() {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
			defer cancel()
			putErr := blobs.PutFile(ctx, objectKey, store.MergedPath(meta), "application/octet-stream")
			if putErr != nil {
				status = "failed"
				detail += "; s3_error=" + putErr.Error()
				storedIn = "local_spool_only"
				_ = meetings.AppendAudit(meeting.AuditEvent{
					MeetingID: meetingID,
					ActorKey:  actor,
					Action:    "local_recording_s3_failed",
					Detail:    putErr.Error(),
				})
			} else {
				detail += "; s3=" + qualified
				storedIn = "s3"
				_ = meetings.AppendAudit(meeting.AuditEvent{
					MeetingID: meetingID,
					ActorKey:  actor,
					Action:    "local_recording_s3_uploaded",
					Detail:    qualified,
				})
			}
		}

		art, artErr := meetings.AddMediaArtifact(meeting.MediaArtifact{
			MeetingID: meetingID,
			Kind:      meeting.KindLocalMic,
			Status:    status,
			ObjectKey: qualified,
			Detail:    detail,
		})
		if artErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": artErr.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"ok":         true,
			"checksum":   final,
			"artifact":   art,
			"object_key": art.ObjectKey,
			"stored_in":  storedIn,
		})
	})
}
