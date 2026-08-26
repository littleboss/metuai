package meeting

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/identity"
)

func registerPipelineTaskRoutes(r *gin.Engine, repo Repository, employeeSecret []byte) {
	workerAuth := identity.WorkerOrEmployeeAuth(employeeSecret, os.Getenv("WORKER_TOKEN"))
	employeeAuth := identity.EmployeeAuth(employeeSecret)

	r.POST("/v1/pipeline/tasks/claim", workerAuth, func(c *gin.Context) {
		var body struct {
			Owner string `json:"owner"`
			Kind  string `json:"kind"`
			Limit int    `json:"limit"`
		}
		_ = c.ShouldBindJSON(&body)
		principal := identity.MustPrincipal(c)
		owner := strings.TrimSpace(body.Owner)
		if owner == "" {
			owner = principal.UserID
		}
		tasks, err := repo.ClaimPipelineTasks(owner, strings.TrimSpace(body.Kind), body.Limit)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"tasks": tasks})
	})

	r.POST("/v1/pipeline/tasks/:taskID/complete", workerAuth, func(c *gin.Context) {
		task, err := CompletePipelineTask(repo, c.Param("taskID"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, task)
	})

	r.POST("/v1/pipeline/tasks/:taskID/fail", workerAuth, func(c *gin.Context) {
		var body struct {
			Error string `json:"error"`
		}
		_ = c.ShouldBindJSON(&body)
		task, err := FailPipelineTask(repo, c.Param("taskID"), body.Error)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, task)
	})

	r.GET("/v1/pipeline/tasks", employeeAuth, func(c *gin.Context) {
		meetingID := strings.TrimSpace(c.Query("meeting_id"))
		if meetingID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "meeting_id required"})
			return
		}
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		principal := identity.MustPrincipal(c)
		if !principal.HasRole("system_admin") && !isMeetingManager(repo, current, principal.UserID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		tasks, err := repo.ListPipelineTasks(meetingID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"tasks": tasks})
	})

	r.GET("/v1/pipeline/manual-review", employeeAuth, func(c *gin.Context) {
		principal := identity.MustPrincipal(c)
		ended, err := repo.ListEnded()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out := make([]gin.H, 0)
		for _, meeting := range ended {
			if meeting.PipelineStage != StageManualReview {
				continue
			}
			if !principal.HasRole("system_admin") && !isMeetingManager(repo, meeting, principal.UserID) {
				continue
			}
			out = append(out, gin.H{
				"id":             meeting.ID,
				"title":          meeting.Title,
				"organizer_id":   meeting.OrganizerID,
				"pipeline_stage": meeting.PipelineStage,
				"ended":          meeting.Ended,
				"created_at":     meeting.CreatedAt,
			})
		}
		dead := make([]PipelineTask, 0)
		if tasks, err := repo.ListPipelineTasks(""); err == nil {
			for _, task := range tasks {
				if task.Status != PipelineTaskDead {
					continue
				}
				meeting, ok := repo.Get(task.MeetingID)
				if !ok {
					continue
				}
				if !principal.HasRole("system_admin") && !isMeetingManager(repo, meeting, principal.UserID) {
					continue
				}
				dead = append(dead, task)
			}
		}
		c.JSON(http.StatusOK, gin.H{"meetings": out, "dead_tasks": dead})
	})

	r.POST("/v1/meetings/:id/pipeline/retry", employeeAuth, func(c *gin.Context) {
		meetingID := c.Param("id")
		current, ok := repo.Get(meetingID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if !requireOrganizer(c, repo, current) {
			return
		}
		task, err := RetryPipeline(repo, meetingID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		principal := identity.MustPrincipal(c)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  PrincipalKey(principal.Kind, principal.UserID, principal.GuestID),
			Action:    "pipeline_retry",
			Detail:    task.ID,
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "pipeline_stage": StageRecordingFinalized, "task": task})
	})
}
