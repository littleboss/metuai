package meeting

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// writeNotesError 把纪要错误写成统一 {error,message}，不泄露会议字段。
func writeNotesError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNoTranscript):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":   "no_transcript",
			"message": "meeting has no transcript; notes cannot be invented",
		})
	case errors.Is(err, ErrAINotConfigured):
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "AI_NOT_CONFIGURED",
			"message": "private LLM is not configured; meetings still work",
		})
	case errors.Is(err, ErrSummaryNotReady):
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "summary_not_ready",
			"message": "meeting summary is not ready yet",
		})
	case errors.Is(err, ErrOwnerMustBeInternal):
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "owner_must_be_internal",
			"message": "action item owner must be an internal user of this meeting",
		})
	case errors.Is(err, ErrMeetingNotEnded):
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "meeting_not_ended",
			"message": "meeting has not ended",
		})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "notes_failed",
			"message": "notes generation failed",
		})
	}
}
