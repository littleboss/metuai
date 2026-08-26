package knowledge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildACLYqlPutsIdentityInQuery(t *testing.T) {
	yql := BuildACLYql("预算); drop", "u-org", "Guest@Example.com", SearchOpts{
		AllowedMeetingIDs:  []string{"m-a"},
		ElevatedMeetingIDs: []string{"m-secret"},
	})
	if !strings.Contains(yql, `allowed_user_ids contains "u-org"`) {
		t.Fatalf("ACL user missing: %s", yql)
	}
	if !strings.Contains(yql, `allowed_guest_emails contains "guest@example.com"`) {
		t.Fatalf("ACL email missing: %s", yql)
	}
	if !strings.Contains(yql, `meeting_id in ("m-a")`) {
		t.Fatalf("allow-list missing: %s", yql)
	}
	if !strings.Contains(yql, `meeting_id in ("m-secret")`) {
		t.Fatalf("elevated meetings missing: %s", yql)
	}
	if strings.Contains(yql, ");") {
		t.Fatalf("query punctuation leaked into YQL: %s", yql)
	}
	if !strings.Contains(yql, "预算") || !strings.Contains(yql, "title contains") {
		t.Fatalf("sanitized query missing: %s", yql)
	}
}

func TestBuildACLYqlDeniesEmptyPrincipal(t *testing.T) {
	yql := BuildACLYql("预算", "", "", SearchOpts{})
	if !strings.Contains(yql, "___no_acl_principal___") {
		t.Fatalf("empty principal must not match the corpus: %s", yql)
	}
}

func TestVespaClientSearchSendsACLYql(t *testing.T) {
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/search") {
			body, _ := io.ReadAll(r.Body)
			captured = string(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"root":{"children":[{"fields":{"meeting_id":"m1","title":"预算会","text":"讨论明年预算","source_type":"summary","source_id":"summary","allowed_user_ids":["u-org"],"allowed_guest_emails":[]}}]}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	idx := NewVespaClient(srv.URL)
	hits, err := idx.Search(context.Background(), "预算", "u-org", "", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Document.MeetingID != "m1" {
		t.Fatalf("hits=%+v", hits)
	}
	var req vespaSearchRequest
	if err := json.Unmarshal([]byte(captured), &req); err != nil {
		t.Fatalf("captured %s: %v", captured, err)
	}
	if !strings.Contains(req.YQL, `allowed_user_ids contains "u-org"`) {
		t.Fatalf("YQL must filter by user before ranking: %s", req.YQL)
	}
	if strings.Contains(req.YQL, "select * from doc where true") {
		t.Fatal("must not search the whole corpus")
	}
}

func TestVespaClientSearchDropsUnauthorizedHits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/search") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"root":{"children":[{"fields":{"meeting_id":"secret","title":"机密","text":"不该看见","source_type":"summary","allowed_user_ids":["u-org"]}}]}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	idx := NewVespaClient(srv.URL)
	hits, err := idx.Search(context.Background(), "机密", "u-other", "", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("defense-in-depth must drop unauthorized YQL hits, got %+v", hits)
	}
}

func TestVespaClientSearchFallsBackToMemory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/search") {
			http.Error(w, "vespa down", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	idx := NewVespaClient(srv.URL)
	_ = idx.Upsert(context.Background(), Document{
		MeetingID:      "m1",
		Title:          "预算会",
		Text:           "讨论明年预算",
		SourceType:     "summary",
		AllowedUserIDs: []string{"u-org"},
	})
	hits, err := idx.Search(context.Background(), "预算", "u-org", "", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("memory fallback should still apply ACL, got %+v", hits)
	}
	blocked, err := idx.Search(context.Background(), "预算", "u-other", "", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 0 {
		t.Fatalf("fallback must not leak, got %+v", blocked)
	}
}
