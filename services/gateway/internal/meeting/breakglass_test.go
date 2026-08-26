package meeting

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"metuai/services/gateway/internal/knowledge"
)

func TestArtifactDownloadWritesAudit(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, "")
	if end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	run := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/pipeline/run-fake", emp, "")
	if run.Code != http.StatusOK {
		t.Fatalf("run-fake %d %s", run.Code, run.Body.String())
	}

	dl := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/artifacts/download", emp, `{"kind":"summary"}`)
	if dl.Code != http.StatusOK {
		t.Fatalf("download %d %s", dl.Code, dl.Body.String())
	}
	audits, _ := store.ListAudit(id)
	found := false
	for _, a := range audits {
		if a.Action == "artifact_download" && a.Detail == "summary" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected artifact_download audit, got %+v", audits)
	}
}

func TestBreakGlassApplyApproveElevatesSearch(t *testing.T) {
	bg := NewBreakGlassStore()
	req, err := bg.Apply("m1", "u-audit", "compliance review")
	if err != nil {
		t.Fatal(err)
	}
	_, err = bg.Approve(req.ID, "u-audit", time.Hour)
	if err == nil {
		t.Fatal("self approve must fail")
	}
	approved, err := bg.Approve(req.ID, "u-approver", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "approved" {
		t.Fatalf("status=%s", approved.Status)
	}
	ids := bg.ElevatedMeetingIDs("u-audit")
	if len(ids) != 1 || ids[0] != "m1" {
		t.Fatalf("elevated=%v", ids)
	}
	if !bg.HasActiveGrant("m1", "u-audit") {
		t.Fatal("expected active grant")
	}
	denied, err := bg.Deny(req.ID, "u-approver")
	if err == nil {
		t.Fatalf("deny approved request should fail, got %+v", denied)
	}
	revoked, err := bg.Revoke(req.ID, "u-approver")
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Status != "expired" {
		t.Fatalf("revoked status=%s", revoked.Status)
	}
	if bg.HasActiveGrant("m1", "u-audit") {
		t.Fatal("revoked grant must not stay active")
	}
}

func TestBreakGlassHTTPFlow(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	org := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, org)
	end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", org, "")
	if end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	run := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/pipeline/run-fake", org, "")
	if run.Code != http.StatusOK {
		t.Fatalf("run-fake %d", run.Code)
	}

	applicant := employeeJWTForRoles(t, secretEmp, "u-audit", "Audit", "audit_admin")
	approver := employeeJWTForRoles(t, secretEmp, "u-boss", "Boss", "system_admin")

	apply := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/break-glass", applicant, `{"reason":"合规抽查"}`)
	if apply.Code != http.StatusOK {
		t.Fatalf("apply %d %s", apply.Code, apply.Body.String())
	}
	var req BreakGlassRequest
	_ = json.Unmarshal(apply.Body.Bytes(), &req)

	self := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/break-glass/"+req.ID+"/approve", applicant, "")
	if self.Code == http.StatusOK {
		t.Fatal("self approve should fail")
	}
	ok := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/break-glass/"+req.ID+"/approve", approver, "")
	if ok.Code != http.StatusOK {
		t.Fatalf("approve %d %s", ok.Code, ok.Body.String())
	}

	dl := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/artifacts/download", applicant, `{"kind":"transcript"}`)
	if dl.Code != http.StatusOK {
		t.Fatalf("break-glass download %d %s", dl.Code, dl.Body.String())
	}

	revoke := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/break-glass/"+req.ID+"/revoke", approver, "")
	if revoke.Code != http.StatusOK {
		t.Fatalf("revoke %d %s", revoke.Code, revoke.Body.String())
	}
	lost := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/artifacts/download", applicant, `{"kind":"transcript"}`)
	if lost.Code != http.StatusForbidden {
		t.Fatalf("revoked grant should lose access, got %d %s", lost.Code, lost.Body.String())
	}

	again := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/break-glass", applicant, `{"reason":"二次抽查"}`)
	if again.Code != http.StatusOK {
		t.Fatalf("second apply %d %s", again.Code, again.Body.String())
	}
	var req2 BreakGlassRequest
	_ = json.Unmarshal(again.Body.Bytes(), &req2)
	deny := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/break-glass/"+req2.ID+"/deny", approver, "")
	if deny.Code != http.StatusOK {
		t.Fatalf("deny %d %s", deny.Code, deny.Body.String())
	}
	still := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/artifacts/download", applicant, `{"kind":"transcript"}`)
	if still.Code != http.StatusForbidden {
		t.Fatalf("denied grant should not elevate, got %d %s", still.Code, still.Body.String())
	}
}

func TestGuestEmailIndexedForACL(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	if err := s.AddGuestEmail(id, "Guest@Example.com"); err != nil {
		t.Fatal(err)
	}
	emails, err := s.ListGuestEmails(id)
	if err != nil || len(emails) != 1 || emails[0] != "guest@example.com" {
		t.Fatalf("emails=%v err=%v", emails, err)
	}
	if err := s.End(id); err != nil {
		t.Fatal(err)
	}
	idx := knowledge.NewMemoryIndex()
	if _, err := RunFakePipeline(s, id, idx); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.Search(context.Background(), "假纪要", "", "guest@example.com", knowledge.SearchOpts{
		AllowedMeetingIDs: []string{id},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("guest email should match indexed ACL")
	}
}

func TestAckedEmployeeCanDownload(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	org := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, org)
	participant := employeeJWTFor(t, secretEmp, "u-2", "Bob")
	_ = store.AckRecording(id, PrincipalKey("employee", "u-2", ""))
	end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", org, "")
	if end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	run := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/pipeline/run-fake", org, "")
	if run.Code != http.StatusOK {
		t.Fatalf("run-fake %d", run.Code)
	}
	dl := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/artifacts/download", participant, `{"kind":"summary"}`)
	if dl.Code != http.StatusOK {
		t.Fatalf("participant download %d %s", dl.Code, dl.Body.String())
	}
	stranger := employeeJWTFor(t, secretEmp, "u-stranger", "X")
	bad := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/artifacts/download", stranger, `{"kind":"summary"}`)
	if bad.Code != http.StatusForbidden {
		t.Fatalf("stranger should be forbidden, got %d", bad.Code)
	}
}
