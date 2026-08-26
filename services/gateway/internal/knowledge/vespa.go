package knowledge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// NewFromEnv 默认 MemoryIndex；设 VESPA_URL 时使用 HTTP 文档/检索客户端（YQL 带 ACL，需已部署 schema）。
func NewFromEnv() Indexer {
	base := strings.TrimSpace(os.Getenv("VESPA_URL"))
	if base == "" {
		return NewMemoryIndex()
	}
	return NewVespaClient(base)
}

// VespaClient 最小 document/v1 + search 客户端（PoC；生产需完整 schema 与 embedding）。
type VespaClient struct {
	base   string
	client *http.Client
	// local 在 Vespa 不可用时仍保证 ACL 语义可测；Upsert 双写。
	local *MemoryIndex
}

func NewVespaClient(baseURL string) *VespaClient {
	return &VespaClient{
		base:   strings.TrimRight(baseURL, "/"),
		client: &http.Client{Timeout: 15 * time.Second},
		local:  NewMemoryIndex(),
	}
}

func (v *VespaClient) Upsert(ctx context.Context, doc Document) error {
	doc = normalizedDocument(doc)
	if err := v.local.Upsert(ctx, doc); err != nil {
		return err
	}
	if doc.Timestamp.IsZero() {
		doc.Timestamp = time.Now().UTC()
	}
	id := url.PathEscape(docKey(doc.MeetingID, doc.SourceType, doc.SourceID))
	endpoint := fmt.Sprintf("%s/document/v1/metuai/doc/docid/%s", v.base, id)
	body := fmt.Sprintf(`{"fields":{"meeting_id":%q,"title":%q,"text":%q,"source_type":%q,"source_id":%q,"allowed_user_ids":%s,"allowed_guest_emails":%s,"timestamp":%d}}`,
		doc.MeetingID, doc.Title, doc.Text, doc.SourceType, doc.SourceID,
		jsonStringArray(doc.AllowedUserIDs), jsonStringArray(doc.AllowedGuestEmails),
		doc.Timestamp.UnixMilli(),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("vespa upsert: %w", err)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("vespa upsert response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("vespa upsert status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	return nil
}

func (v *VespaClient) DeleteMeeting(ctx context.Context, meetingID string) error {
	keys := v.local.keysForMeeting(meetingID)
	var first error
	for _, id := range keys {
		endpoint := fmt.Sprintf("%s/document/v1/metuai/doc/docid/%s", v.base, url.PathEscape(id))
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		resp, err := v.client.Do(req)
		if err != nil {
			if first == nil {
				first = fmt.Errorf("vespa delete: %w", err)
			}
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
		resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound && first == nil {
			first = fmt.Errorf("vespa delete status %d", resp.StatusCode)
		}
	}
	if err := v.local.DeleteMeeting(ctx, meetingID); err != nil && first == nil {
		return err
	}
	return first
}

func (v *VespaClient) Search(ctx context.Context, query, viewerUserID, viewerEmail string, opts SearchOpts) ([]SearchHit, error) {
	// ACL 写进 YQL；命中后再做一次 viewerAllowed，防止集群误配把别人的文档漏出来。
	// Vespa 不可用时退回本地副本（本地 Search 同样先滤 ACL）。不是 embedding/ANN。
	hits, err := v.searchYQL(ctx, BuildACLYql(query, viewerUserID, viewerEmail, opts))
	if err != nil {
		return v.local.Search(ctx, query, viewerUserID, viewerEmail, opts)
	}
	return applySearchACL(hits, viewerUserID, viewerEmail, opts), nil
}

func jsonStringArray(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(xs))
	for _, x := range xs {
		parts = append(parts, fmt.Sprintf("%q", x))
	}
	return "[" + strings.Join(parts, ",") + "]"
}
