package meeting

import (
	"context"
	"log"
	"time"
)

// idleFinalizeTimeout 给「自动结束时收尾 Egress」一个上限。
// 后台 goroutine 没有请求上下文可继承，不设预算的话一次 LiveKit 卡死
// 就会把整个巡检循环挂住，后面的会议再也不会被结束。
const idleFinalizeTimeout = 60 * time.Second

const occupancyCheckTimeout = 8 * time.Second

// OccupancyCheck 询问房间里是否还有真人在线。
// 返回 error 时巡检应跳过这场会（不能证明无人，宁可多留一会儿）。
type OccupancyCheck func(ctx context.Context, roomID string) (occupied bool, err error)

// StartIdleReaper 定期结束长时间无活动的会议，避免最后一人短暂断线误杀。
// idleFor 默认应对齐架构文档的 10 分钟。
// occupy 可为 nil：仅看客户端心跳；有 LiveKit 时应传入真实在房人数。
func StartIdleReaper(repo Repository, idleFor, every time.Duration, egressRT *EgressRuntime, occupy OccupancyCheck, stop <-chan struct{}) {
	if idleFor <= 0 {
		idleFor = 10 * time.Minute
	}
	if every <= 0 {
		every = 30 * time.Second
	}
	ticker := time.NewTicker(every)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				endIdleMeetings(repo, idleFor, egressRT, occupy)
			}
		}
	}()
}

func endIdleMeetings(repo Repository, idleFor time.Duration, egressRT *EgressRuntime, occupy OccupancyCheck) {
	active, err := repo.ListActive()
	if err != nil {
		log.Printf("idle reaper list: %v", err)
		return
	}
	cutoff := time.Now().UTC().Add(-idleFor)
	for _, m := range active {
		if m.LastActiveAt.After(cutoff) {
			continue
		}
		if occupy != nil {
			ctx, cancel := context.WithTimeout(context.Background(), occupancyCheckTimeout)
			occupied, occErr := occupy(ctx, m.ID)
			cancel()
			if occErr != nil {
				log.Printf("idle reaper occupancy %s: %v", m.ID, occErr)
				continue
			}
			if occupied {
				_ = repo.TouchActivity(m.ID)
				continue
			}
		}
		if err := repo.End(m.ID); err != nil {
			log.Printf("idle reaper end %s: %v", m.ID, err)
			continue
		}
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: m.ID,
			ActorKey:  "system",
			Action:    "meeting_idle_ended",
			Detail:    idleFor.String(),
		})
		EnqueueAfterEnd(repo, m.ID)
		ctx, cancel := context.WithTimeout(context.Background(), idleFinalizeTimeout)
		egressRT.FinalizeOrPlan(ctx, repo, m.ID, "", "idle end")
		cancel()
	}
}
