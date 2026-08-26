// Package knowledge 是会后知识检索副本（架构：PG 权威，此处为带 ACL 的检索索引）。
// PoC 默认 MemoryIndex；设 VESPA_URL 时可换 HTTP 客户端（见 vespa.go）。
package knowledge

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Document 对齐 tech-stack §6.2 的最小可检索文档。
type Document struct {
	MeetingID          string    `json:"meeting_id"`
	Title              string    `json:"title"`
	Text               string    `json:"text"`
	SourceType         string    `json:"source_type"` // summary | transcript
	SourceID           string    `json:"source_id"`
	AllowedUserIDs     []string  `json:"allowed_user_ids"`
	AllowedGuestEmails []string  `json:"allowed_guest_emails"`
	Timestamp          time.Time `json:"timestamp"`
}

// SearchHit 是带权限过滤后的命中。
type SearchHit struct {
	Document Document `json:"document"`
	Snippet  string   `json:"snippet"`
}

// Indexer 写入与按 ACL 查询。Search 必须在索引侧按 allowedMeetingIDs / user 过滤，
// 禁止「先全库再应用层滤」（PoC Memory 也遵守：先滤会议集合再匹配关键词）。
type Indexer interface {
	Upsert(ctx context.Context, doc Document) error
	DeleteMeeting(ctx context.Context, meetingID string) error
	Search(ctx context.Context, query string, viewerUserID string, viewerEmail string, opts SearchOpts) ([]SearchHit, error)
}

// MemoryIndex 进程内索引，供单测与无 Vespa 时的默认后端。
type MemoryIndex struct {
	mu   sync.RWMutex
	docs map[string]Document // key = meeting_id + ":" + source_type
}

func NewMemoryIndex() *MemoryIndex {
	return &MemoryIndex{docs: map[string]Document{}}
}

func docKey(meetingID, sourceType string) string {
	return meetingID + ":" + sourceType
}

func (m *MemoryIndex) Upsert(_ context.Context, doc Document) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if doc.Timestamp.IsZero() {
		doc.Timestamp = time.Now().UTC()
	}
	if doc.SourceType == "" {
		doc.SourceType = "summary"
	}
	m.docs[docKey(doc.MeetingID, doc.SourceType)] = doc
	return nil
}

func (m *MemoryIndex) DeleteMeeting(_ context.Context, meetingID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, d := range m.docs {
		if d.MeetingID == meetingID {
			delete(m.docs, k)
		}
	}
	return nil
}

func viewerAllowed(doc Document, userID, email string) bool {
	if userID != "" {
		for _, id := range doc.AllowedUserIDs {
			if id == userID {
				return true
			}
		}
	}
	if email != "" {
		el := strings.ToLower(email)
		for _, g := range doc.AllowedGuestEmails {
			if strings.ToLower(g) == el {
				return true
			}
		}
	}
	return false
}

// SearchOpts 控制会议 allow-list 与破窗抬权。
type SearchOpts struct {
	AllowedMeetingIDs  []string
	ElevatedMeetingIDs []string // 破窗：跳过文档 ACL，但仍可受 AllowedMeetingIDs 约束
}

func (m *MemoryIndex) Search(ctx context.Context, query, viewerUserID, viewerEmail string, opts SearchOpts) ([]SearchHit, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	allow := map[string]struct{}{}
	for _, id := range opts.AllowedMeetingIDs {
		allow[id] = struct{}{}
	}
	elevated := map[string]struct{}{}
	for _, id := range opts.ElevatedMeetingIDs {
		elevated[id] = struct{}{}
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]SearchHit, 0)
	for _, doc := range m.docs {
		if len(allow) > 0 {
			if _, ok := allow[doc.MeetingID]; !ok {
				continue
			}
		}
		_, elev := elevated[doc.MeetingID]
		if !elev && !viewerAllowed(doc, viewerUserID, viewerEmail) {
			continue
		}
		hay := strings.ToLower(doc.Title + " " + doc.Text)
		if q != "" && !strings.Contains(hay, q) {
			continue
		}
		snippet := doc.Text
		if len(snippet) > 160 {
			snippet = snippet[:160] + "…"
		}
		out = append(out, SearchHit{Document: doc, Snippet: snippet})
	}
	return out, nil
}

// BackendName 供 /healthz。
func BackendName(idx Indexer) string {
	if idx == nil {
		return "none"
	}
	switch idx.(type) {
	case *MemoryIndex:
		return "memory"
	case *VespaClient:
		return "vespa+memory"
	default:
		return "custom"
	}
}
