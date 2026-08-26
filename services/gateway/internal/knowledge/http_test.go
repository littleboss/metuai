package knowledge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"metuai/services/gateway/internal/identity"
)

func TestSearchHTTPFiltersByACL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	idx := NewMemoryIndex()
	_ = idx.Upsert(nil, Document{
		MeetingID:      "m1",
		Title:          "预算",
		Text:           "明年预算讨论",
		SourceType:     "summary",
		AllowedUserIDs: []string{"u-1"},
		// 员工只能用 user_id 授权，不能因为企业邮箱碰巧在嘉宾 ACL 中而越权。
		AllowedGuestEmails: []string{"u-2@corp.local"},
	})
	secret := []byte("emp")
	r := gin.New()
	RegisterRoutes(r, idx, secret, []byte("gst"), nil, nil)

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u-1", "kind": identity.KindEmployee, "email": "u-1@corp.local",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	emp, _ := tok.SignedString(secret)

	req := httptest.NewRequest(http.MethodGet, "/v1/knowledge/search?q=%E9%A2%84%E7%AE%97", nil)
	req.Header.Set("Authorization", "Bearer "+emp)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Hits []SearchHit `json:"hits"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Hits) != 1 {
		t.Fatalf("want 1 hit, got %+v", body)
	}

	tok2 := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u-2", "kind": identity.KindEmployee, "email": "u-2@corp.local",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	other, _ := tok2.SignedString(secret)
	req2 := httptest.NewRequest(http.MethodGet, "/v1/knowledge/search?q=%E9%A2%84%E7%AE%97", nil)
	req2.Header.Set("Authorization", "Bearer "+other)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	_ = json.Unmarshal(w2.Body.Bytes(), &body)
	if len(body.Hits) != 0 {
		t.Fatalf("other employee must not see hits, got %+v", body)
	}
}

func TestSearchHTTPAllowsVerifiedGuestEmailOnlyWithinMeeting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	idx := NewMemoryIndex()
	if err := idx.Upsert(nil, Document{
		MeetingID:          "m1",
		Title:              "预算",
		Text:               "明年预算讨论",
		SourceType:         "summary",
		AllowedGuestEmails: []string{"guest@example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	guestSecret := []byte("gst")
	RegisterRoutes(r, idx, []byte("emp"), guestSecret, nil, nil)

	verified, err := identity.IssueGuestSession(identity.Principal{
		Kind:      identity.KindGuest,
		GuestID:   "g1",
		MeetingID: "m1",
		Email:     "guest@example.com",
	}, guestSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/knowledge/search?q=%E9%A2%84%E7%AE%97", nil)
	req.Header.Set("Authorization", "Bearer "+verified)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var body struct {
		Hits []SearchHit `json:"hits"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusOK || len(body.Hits) != 1 {
		t.Fatalf("verified guest search %d %+v", w.Code, body)
	}

	unverified, err := identity.IssueGuestSession(identity.Principal{
		Kind:      identity.KindGuest,
		GuestID:   "g1",
		MeetingID: "m1",
	}, guestSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/knowledge/search?q=%E9%A2%84%E7%AE%97", nil)
	req.Header.Set("Authorization", "Bearer "+unverified)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unverified guest search %d %s", w.Code, w.Body.String())
	}
}
