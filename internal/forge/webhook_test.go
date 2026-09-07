package forge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	good := sign("s3cr3t", body)

	if !VerifySignature("s3cr3t", body, good) {
		t.Fatal("valid signature rejected")
	}
	if VerifySignature("s3cr3t", body, sign("wrong", body)) {
		t.Fatal("signature from a different key accepted")
	}
	if VerifySignature("s3cr3t", append(body, '!'), good) {
		t.Fatal("tampered body accepted")
	}
	if VerifySignature("", body, good) {
		t.Fatal("empty secret must never verify")
	}
	if VerifySignature("s3cr3t", body, "deadbeef") {
		t.Fatal("malformed header accepted")
	}
}

const reviewChangesRequested = `{
  "action": "submitted",
  "review": { "body": "the DELETE endpoint accepts a negative id", "state": "changes_requested",
              "user": {"login": "octocat"} },
  "pull_request": { "html_url": "https://github.com/me/repo/pull/7", "head": {"ref": "asf/run-abc12345"} },
  "repository": { "full_name": "me/repo" }
}`

const reviewApproved = `{
  "action": "submitted",
  "review": { "body": "", "state": "approved", "user": {"login": "octocat"} },
  "pull_request": { "html_url": "https://github.com/me/repo/pull/7", "head": {"ref": "asf/run-abc12345"} },
  "repository": { "full_name": "me/repo" }
}`

const reviewCommented = `{
  "action": "submitted",
  "review": { "body": "", "state": "commented", "user": {"login": "octocat"} },
  "pull_request": { "html_url": "https://github.com/me/repo/pull/7", "head": {"ref": "asf/run-abc12345"} },
  "repository": { "full_name": "me/repo" }
}`

const reviewComment = `{
  "action": "created",
  "comment": { "body": "rename this", "path": "internal/api/handlers.go", "line": 42,
               "user": {"login": "octocat"} },
  "pull_request": { "html_url": "https://github.com/me/repo/pull/7", "head": {"ref": "asf/run-abc12345"} },
  "repository": { "full_name": "me/repo" }
}`

func TestParsePRReview(t *testing.T) {
	r, ok, err := ParsePRReview("pull_request_review", []byte(reviewChangesRequested))
	if err != nil || !ok {
		t.Fatalf("changes_requested: ok=%v err=%v", ok, err)
	}
	if r.State != "changes_requested" || r.PRURL != "https://github.com/me/repo/pull/7" ||
		r.HeadRef != "asf/run-abc12345" || r.RepoFullName != "me/repo" {
		t.Fatalf("fields lost: %+v", r)
	}
	if len(r.Comments) != 1 || r.Comments[0].Body != "the DELETE endpoint accepts a negative id" {
		t.Fatalf("comment lost: %+v", r.Comments)
	}

	if r, ok, _ := ParsePRReview("pull_request_review", []byte(reviewApproved)); !ok || r.State != "approved" {
		t.Fatalf("approved: ok=%v r=%+v", ok, r)
	}

	// a plain "commented" review with no body is not actionable
	if _, ok, _ := ParsePRReview("pull_request_review", []byte(reviewCommented)); ok {
		t.Fatal("empty 'commented' review should be ignored")
	}

	r, ok, err = ParsePRReview("pull_request_review_comment", []byte(reviewComment))
	if err != nil || !ok {
		t.Fatalf("review comment: ok=%v err=%v", ok, err)
	}
	if r.State != "changes_requested" || r.Comments[0].Path != "internal/api/handlers.go" || r.Comments[0].Line != 42 {
		t.Fatalf("inline comment lost detail: %+v", r)
	}

	// unrelated events are ignored, not errors
	if _, ok, err := ParsePRReview("ping", []byte(`{"zen":"hi"}`)); ok || err != nil {
		t.Fatalf("ping: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := ParsePRReview("pull_request", []byte(`{"action":"opened"}`)); ok {
		t.Fatal("pull_request event should be ignored")
	}
}
