package modelrelease

import "testing"

func TestApprovalSeparationAndRequestValidation(t *testing.T) {
	r := Release{ApplicantID: "submitter", ModelOwnerID: "owner", TenantID: "team"}
	for _, a := range []Actor{{ID: "submitter", SuperAdmin: true}, {ID: "owner", SuperAdmin: true}, {ID: "other", TenantID: "else", TenantAdmin: true}, {ID: "other", TenantID: "team"}} {
		if CanReview(r, a) {
			t.Fatalf("unauthorized reviewer %+v", a)
		}
	}
	for _, a := range []Actor{{ID: "other", SuperAdmin: true}, {ID: "other", TenantID: "team", TenantAdmin: true}} {
		if !CanReview(r, a) {
			t.Fatalf("reviewer denied %+v", a)
		}
	}
	good := Request{ModelID: "model", VersionID: "version", EvaluationID: "evaluation", Reason: "Verified evaluation", IdempotencyKey: "once"}
	if ValidateRequest(good) != nil {
		t.Fatal("valid request rejected")
	}
	for _, change := range []func(*Request){func(r *Request) { r.Reason = "  " }, func(r *Request) { r.EvaluationID = "" }, func(r *Request) { r.IdempotencyKey = "" }} {
		r := good
		change(&r)
		if ValidateRequest(r) == nil {
			t.Fatalf("invalid request accepted %+v", r)
		}
	}
}
