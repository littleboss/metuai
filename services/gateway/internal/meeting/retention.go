package meeting

import (
	"context"
	"log"
	"strings"
	"time"

	"metuai/services/gateway/internal/knowledge"
)

const (
	defaultMediaTTL      = 90 * 24 * time.Hour
	defaultVideoTTL      = 30 * 24 * time.Hour
	defaultKnowledgeTTL  = 365 * 24 * time.Hour
	StageKnowledgePurged = "KNOWLEDGE_EXPIRED"
)

// RetentionPolicy 是架构 §7.4 的两套时钟：媒体与知识分开；画面允许更短。
type RetentionPolicy struct {
	MediaTTLSeconds     int64     `json:"media_ttl_seconds"`
	VideoTTLSeconds     int64     `json:"video_ttl_seconds"`
	KnowledgeTTLSeconds int64     `json:"knowledge_ttl_seconds"`
	UpdatedAt           time.Time `json:"updated_at"`
	UpdatedBy           string    `json:"updated_by,omitempty"`
}

func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		MediaTTLSeconds:     int64(defaultMediaTTL / time.Second),
		VideoTTLSeconds:     int64(defaultVideoTTL / time.Second),
		KnowledgeTTLSeconds: int64(defaultKnowledgeTTL / time.Second),
	}
}

func (p RetentionPolicy) mediaTTL() time.Duration {
	return time.Duration(p.MediaTTLSeconds) * time.Second
}
func (p RetentionPolicy) videoTTL() time.Duration {
	return time.Duration(p.VideoTTLSeconds) * time.Second
}
func (p RetentionPolicy) knowledgeTTL() time.Duration {
	return time.Duration(p.KnowledgeTTLSeconds) * time.Second
}

func (p RetentionPolicy) normalize() RetentionPolicy {
	if p.MediaTTLSeconds < 0 {
		p.MediaTTLSeconds = 0
	}
	if p.VideoTTLSeconds < 0 {
		p.VideoTTLSeconds = 0
	}
	if p.KnowledgeTTLSeconds < 0 {
		p.KnowledgeTTLSeconds = 0
	}
	return p
}

// ObjectDeleter 删除对象存储中的媒体文件；未接线时只落库标记 purged。
type ObjectDeleter interface {
	DeleteObject(ctx context.Context, objectKey string) error
}

func StartRetentionSweeper(repo Repository, idx knowledge.Indexer, blobs ObjectDeleter, every time.Duration, stop <-chan struct{}) {
	if every <= 0 {
		every = time.Hour
	}
	ticker := time.NewTicker(every)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := SweepRetention(context.Background(), repo, idx, blobs); err != nil {
					log.Printf("retention sweep: %v", err)
				}
			}
		}
	}()
}

// SweepRetention 按结束时间分别清视频、其余媒体、知识索引与转写/纪要。
func SweepRetention(ctx context.Context, repo Repository, idx knowledge.Indexer, blobs ObjectDeleter) error {
	policy, err := repo.GetRetentionPolicy()
	if err != nil {
		return err
	}
	ended, err := repo.ListEnded()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, m := range ended {
		if m.EndedAt == nil {
			continue
		}
		endedAt := m.EndedAt.UTC()
		if policy.videoTTL() > 0 && now.After(endedAt.Add(policy.videoTTL())) {
			if err := purgeMedia(ctx, repo, blobs, m.ID, []string{KindRoomVideo}); err != nil {
				log.Printf("retention video %s: %v", m.ID, err)
			}
		}
		if policy.mediaTTL() > 0 && now.After(endedAt.Add(policy.mediaTTL())) {
			if err := purgeMedia(ctx, repo, blobs, m.ID, nil); err != nil {
				log.Printf("retention media %s: %v", m.ID, err)
			}
		}
		if policy.knowledgeTTL() > 0 && now.After(endedAt.Add(policy.knowledgeTTL())) {
			if err := repo.PurgeKnowledge(m.ID); err != nil {
				log.Printf("retention knowledge store %s: %v", m.ID, err)
				continue
			}
			if idx != nil {
				if err := idx.DeleteMeeting(ctx, m.ID); err != nil {
					log.Printf("retention knowledge index %s: %v", m.ID, err)
				}
			}
			_ = repo.AppendAudit(AuditEvent{
				MeetingID: m.ID,
				ActorKey:  "system",
				Action:    "retention_knowledge_purged",
			})
		}
	}
	return nil
}

func mediaKindWanted(kind string, kinds []string) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

func purgeMedia(ctx context.Context, repo Repository, blobs ObjectDeleter, meetingID string, kinds []string) error {
	purged, err := repo.PurgeMediaKinds(meetingID, kinds)
	if err != nil {
		return err
	}
	for _, art := range purged {
		key := strings.TrimSpace(art.ObjectKey)
		if blobs == nil || key == "" {
			continue
		}
		if err := blobs.DeleteObject(ctx, key); err != nil {
			log.Printf("retention delete object %s: %v", key, err)
		}
	}
	if len(purged) > 0 {
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  "system",
			Action:    "retention_media_purged",
			Detail:    strings.Join(kinds, ","),
		})
	}
	return nil
}
