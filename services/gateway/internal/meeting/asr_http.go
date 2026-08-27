package meeting

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// writeASRError 把转写生成错误写成统一 {error,message}，不泄露会议字段。
func writeASRError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNoAudio):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":   "no_audio",
			"message": "meeting has no authoritative audio for transcription",
		})
	case errors.Is(err, ErrASRNotConfigured):
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "ASR_NOT_CONFIGURED",
			"message": "private ASR is not configured; meetings still work",
		})
	case errors.Is(err, ErrMeetingNotEnded):
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "meeting_not_ended",
			"message": "meeting has not ended",
		})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "transcript_failed",
			"message": "transcript generation failed",
		})
	}
}
