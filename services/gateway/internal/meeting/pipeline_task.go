package meeting

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// 会后任务权威状态（架构 P2：PostgreSQL 保存任务，而不是只靠 pipeline_stage）。
// PoC 不假装已接 Dapr；Worker 用租约领取任务，失败可重试，超限进死信 + MANUAL_REVIEW。
const (
	PipelineTaskQueued    = "queued"
	PipelineTaskLeased    = "leased"
	PipelineTaskSucceeded = "succeeded"
	PipelineTaskFailed    = "failed"
	PipelineTaskDead      = "dead"

	PipelineKindFake = "fake"
	PipelineKindASR  = "asr"

	pipelineTaskMaxAttempts = 3
	pipelineTaskLease       = 5 * time.Minute
)

// PipelineTask 是一场会的一条会后作业（假流水线或 ASR）。
type PipelineTask struct {
	ID          string     `json:"id"`
	MeetingID   string     `json:"meeting_id"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"max_attempts"`
	LeaseOwner  string     `json:"lease_owner,omitempty"`
	LeaseUntil  *time.Time `json:"lease_until,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func DefaultPipelineTaskKind() string {
	kind := strings.ToLower(strings.TrimSpace(os.Getenv("PIPELINE_TASK_KIND")))
	switch kind {
	case PipelineKindASR:
		return PipelineKindASR
	default:
		return PipelineKindFake
	}
}

func pipelineTaskClaimable(task PipelineTask, now time.Time) bool {
	if task.Attempts >= task.MaxAttempts {
		return false
	}
	switch task.Status {
	case PipelineTaskQueued, PipelineTaskFailed:
		return true
	case PipelineTaskLeased:
		return task.LeaseUntil != nil && now.After(*task.LeaseUntil)
	default:
		return false
	}
}

// EnqueuePipelineTask 在会议结束后放入作业。已有未完成同 kind 任务时复用，避免重复跑。
func EnqueuePipelineTask(repo Repository, meetingID, kind string) (PipelineTask, error) {
	if _, ok := repo.Get(meetingID); !ok {
		return PipelineTask{}, fmt.Errorf("meeting not found")
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = DefaultPipelineTaskKind()
	}
	existing, err := repo.ListPipelineTasks(meetingID)
	if err != nil {
		return PipelineTask{}, err
	}
	for _, task := range existing {
		if task.Kind == kind && (task.Status == PipelineTaskQueued || task.Status == PipelineTaskLeased || task.Status == PipelineTaskFailed) {
			return task, nil
		}
	}
	return repo.SavePipelineTask(PipelineTask{
		MeetingID:   meetingID,
		Kind:        kind,
		Status:      PipelineTaskQueued,
		MaxAttempts: pipelineTaskMaxAttempts,
	})
}

func EnqueueAfterEnd(repo Repository, meetingID string) {
	_, _ = EnqueuePipelineTask(repo, meetingID, DefaultPipelineTaskKind())
}

func CompletePipelineTask(repo Repository, id string) (PipelineTask, error) {
	task, ok := repo.GetPipelineTask(id)
	if !ok {
		return PipelineTask{}, fmt.Errorf("pipeline task not found")
	}
	if task.Status == PipelineTaskSucceeded || task.Status == PipelineTaskDead {
		return task, nil
	}
	task.Status = PipelineTaskSucceeded
	task.LeaseOwner = ""
	task.LeaseUntil = nil
	task.LastError = ""
	if err := repo.UpdatePipelineTask(task); err != nil {
		return PipelineTask{}, err
	}
	return task, nil
}

func FailPipelineTask(repo Repository, id, errMsg string) (PipelineTask, error) {
	task, ok := repo.GetPipelineTask(id)
	if !ok {
		return PipelineTask{}, fmt.Errorf("pipeline task not found")
	}
	if task.Status == PipelineTaskSucceeded || task.Status == PipelineTaskDead {
		return task, nil
	}
	task.Attempts++
	task.LastError = strings.TrimSpace(errMsg)
	task.LeaseOwner = ""
	task.LeaseUntil = nil
	if task.Attempts >= task.MaxAttempts {
		task.Status = PipelineTaskDead
		_ = repo.SetPipelineStage(task.MeetingID, StageManualReview)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: task.MeetingID,
			ActorKey:  "system:worker",
			Action:    "pipeline_dead_letter",
			Detail:    task.Kind + ": " + task.LastError,
		})
	} else {
		task.Status = PipelineTaskFailed
		_ = repo.SetPipelineStage(task.MeetingID, StageRetryableError)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: task.MeetingID,
			ActorKey:  "system:worker",
			Action:    "pipeline_retryable_error",
			Detail:    fmt.Sprintf("%s attempt %d/%d: %s", task.Kind, task.Attempts, task.MaxAttempts, task.LastError),
		})
	}
	if err := repo.UpdatePipelineTask(task); err != nil {
		return PipelineTask{}, err
	}
	return task, nil
}

func CompleteOpenPipelineTasks(repo Repository, meetingID, kind string) {
	tasks, err := repo.ListPipelineTasks(meetingID)
	if err != nil {
		return
	}
	for _, task := range tasks {
		if kind != "" && task.Kind != kind {
			continue
		}
		if task.Status == PipelineTaskQueued || task.Status == PipelineTaskLeased || task.Status == PipelineTaskFailed {
			_, _ = CompletePipelineTask(repo, task.ID)
		}
	}
}

// RetryPipeline 把人工复核/可重试失败重新排队，给 Worker 再领一次。
func RetryPipeline(repo Repository, meetingID string) (PipelineTask, error) {
	current, ok := repo.Get(meetingID)
	if !ok {
		return PipelineTask{}, fmt.Errorf("meeting not found")
	}
	if !current.Ended {
		return PipelineTask{}, fmt.Errorf("meeting_not_ended")
	}
	switch current.PipelineStage {
	case StageManualReview, StageRetryableError, StageRecordingFinalized, "":
	default:
		return PipelineTask{}, fmt.Errorf("pipeline_not_retryable")
	}
	if err := repo.SetPipelineStage(meetingID, StageRecordingFinalized); err != nil {
		return PipelineTask{}, err
	}
	kind := DefaultPipelineTaskKind()
	tasks, err := repo.ListPipelineTasks(meetingID)
	if err != nil {
		return PipelineTask{}, err
	}
	for _, task := range tasks {
		if task.Kind != kind || task.Status == PipelineTaskSucceeded {
			continue
		}
		task.Status = PipelineTaskQueued
		task.Attempts = 0
		task.LastError = ""
		task.LeaseOwner = ""
		task.LeaseUntil = nil
		if err := repo.UpdatePipelineTask(task); err != nil {
			return PipelineTask{}, err
		}
		return task, nil
	}
	return EnqueuePipelineTask(repo, meetingID, kind)
}
