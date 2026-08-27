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
	default:
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "notes_failed",
			"message": "notes generation failed",
		})
	}
}
