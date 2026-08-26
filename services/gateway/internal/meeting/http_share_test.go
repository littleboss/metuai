package meeting

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestLivekitTokenDeviceIdentity(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	_ = store.AckRecording(id, PrincipalKey("employee", "u-1", ""))

	lt := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/livekit-token", emp, `{"device_id":"phone-1"}`)
	if lt.Code != http.StatusOK {
		t.Fatalf("token %d %s", lt.Code, lt.Body.String())
	}
	var body struct {
		Identity string `json:"identity"`
	}
	if err := json.Unmarshal(lt.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Identity != "u-1~phone-1" {
		t.Fatalf("identity=%q", body.Identity)
	}
}

func TestKickDeviceIdentityBlocksAllDevices(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	_ = store.AckRecording(id, PrincipalKey("employee", "u-1", ""))
	other := employeeJWTFor(t, secretEmp, "u-2", "Carol")
	_ = store.AckRecording(id, PrincipalKey("employee", "u-2", ""))

	kick := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/kick", emp, `{"identity":"u-2~laptop"}`)
	if kick.Code != http.StatusOK {
		t.Fatalf("kick %d %s", kick.Code, kick.Body.String())
	}
	blocked := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/livekit-token", other, `{"device_id":"phone"}`)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("kicked device should be blocked, got %d %s", blocked.Code, blocked.Body.String())
	}

	organizerKick := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/kick", emp, `{"identity":"u-1~watch"}`)
	if organizerKick.Code != http.StatusBadRequest {
		t.Fatalf("cannot kick organizer device, got %d %s", organizerKick.Code, organizerKick.Body.String())
	}
}

func TestShareEmailACLRequiresVerification(t *testing.T) {
	sender := &capturedVerificationSender{}
	r, store, secretEmp, _ := testRouterWithEgressAndVerification(t, true, nil, sender)
	organizer := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, organizer)

	stranger := employeeJWTFor(t, secretEmp, "u-stranger", "Eve")
	if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/shared-readers", stranger, `{"email":"reader@example.com"}`); got.Code != http.StatusForbidden {
		t.Fatalf("stranger share %d %s", got.Code, got.Body.String())
	}

	add := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/shared-readers", organizer, `{"email":"Reader@Example.com"}`)
	if add.Code != http.StatusOK {
		t.Fatalf("add share %d %s", add.Code, add.Body.String())
	}

	if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/shared-readers/verify", "", `{"email":"other@example.com"}`); got.Code != http.StatusForbidden {
		t.Fatalf("unlisted verify %d %s", got.Code, got.Body.String())
	}
	if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/shared-readers/verify", "", `{"email":"reader@example.com"}`); got.Code != http.StatusOK {
		t.Fatalf("share verify %d %s", got.Code, got.Body.String())
	}
	if sender.mail.To != "reader@example.com" || len(sender.mail.Code) != 6 {
		t.Fatalf("mail = %+v", sender.mail)
	}

	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", organizer, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	if run := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/pipeline/run-fake", organizer, ""); run.Code != http.StatusOK {
		t.Fatalf("run-fake %d %s", run.Code, run.Body.String())
	}

	confirm := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/shared-readers/confirm", "",
		`{"email":"reader@example.com","code":"`+sender.mail.Code+`"}`)
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm %d %s", confirm.Code, confirm.Body.String())
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		Email       string `json:"email"`
	}
	if err := json.Unmarshal(confirm.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	summary := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", payload.AccessToken, "")
	if summary.Code != http.StatusOK {
		t.Fatalf("shared reader summary %d %s", summary.Code, summary.Body.String())
	}
	emails, err := store.ListGuestEmails(id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, email := range emails {
		if email == "reader@example.com" {
			found = true
		}
	}
	if !found {
		t.Fatal("verified share should write guest email ACL")
	}

	if del := doJSON(t, r, http.MethodDelete, "/v1/meetings/"+id+"/shared-readers?email=reader@example.com", organizer, ""); del.Code != http.StatusOK {
		t.Fatalf("unshare %d %s", del.Code, del.Body.String())
	}
	emails, err = store.ListGuestEmails(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range emails {
		if email == "reader@example.com" {
			t.Fatal("shared-only email should be removed from ACL")
		}
	}
	blocked := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", payload.AccessToken, "")
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("unshared reader should lose access, got %d %s", blocked.Code, blocked.Body.String())
	}
}
