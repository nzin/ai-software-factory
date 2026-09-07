package forge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Comment is one human note left on a pull request.
type Comment struct {
	Body   string
	Path   string
	Line   int
	Author string
}

// PRReview is a normalised GitHub PR review event. The coordinator matches it to
// a run by PRURL (or HeadRef + RepoFullName) and feeds the comments into the
// same path as an in-UI review.
type PRReview struct {
	RepoFullName string // "owner/repo"
	PRURL        string // pull_request.html_url
	HeadRef      string // pull_request.head.ref  ("asf/run-…")
	State        string // approved | changes_requested | commented
	Author       string
	Comments     []Comment
}

// Actionable reports whether this review should drive the run (a change request,
// or an approval). A plain comment with nothing actionable returns false.
func (r PRReview) Actionable() bool {
	switch r.State {
	case "approved", "changes_requested":
		return true
	}
	return len(r.Comments) > 0
}

// VerifySignature checks the GitHub X-Hub-Signature-256 header ("sha256=<hex>")
// against an HMAC-SHA256 of body keyed by secret. An empty secret never verifies.
func VerifySignature(secret string, body []byte, header string) bool {
	if secret == "" || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(header), []byte(want))
}

// ParsePRReview normalises a GitHub webhook delivery. It handles
// X-GitHub-Event ∈ {pull_request_review, pull_request_review_comment}; anything
// else (ping, pull_request, …) returns (nil, false, nil) so the caller can
// 202-ignore it.
func ParsePRReview(event string, body []byte) (*PRReview, bool, error) {
	switch event {
	case "pull_request_review":
		return parseReview(body)
	case "pull_request_review_comment":
		return parseReviewComment(body)
	default:
		return nil, false, nil
	}
}

type ghPR struct {
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

type ghRepo struct {
	FullName string `json:"full_name"`
}

type ghUser struct {
	Login string `json:"login"`
}

func parseReview(body []byte) (*PRReview, bool, error) {
	var p struct {
		Action string `json:"action"`
		Review struct {
			Body  string `json:"body"`
			State string `json:"state"`
			User  ghUser `json:"user"`
		} `json:"review"`
		PullRequest ghPR   `json:"pull_request"`
		Repository  ghRepo `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, false, fmt.Errorf("forge: bad pull_request_review payload: %w", err)
	}
	if p.Action != "submitted" {
		return nil, false, nil
	}
	r := &PRReview{
		RepoFullName: p.Repository.FullName,
		PRURL:        p.PullRequest.HTMLURL,
		HeadRef:      p.PullRequest.Head.Ref,
		State:        strings.ToLower(p.Review.State),
		Author:       p.Review.User.Login,
	}
	if b := strings.TrimSpace(p.Review.Body); b != "" {
		r.Comments = append(r.Comments, Comment{Body: b, Author: r.Author})
	}
	if !r.Actionable() {
		return nil, false, nil
	}
	return r, true, nil
}

func parseReviewComment(body []byte) (*PRReview, bool, error) {
	var p struct {
		Action  string `json:"action"`
		Comment struct {
			Body         string `json:"body"`
			Path         string `json:"path"`
			Line         int    `json:"line"`
			OriginalLine int    `json:"original_line"`
			User         ghUser `json:"user"`
		} `json:"comment"`
		PullRequest ghPR   `json:"pull_request"`
		Repository  ghRepo `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, false, fmt.Errorf("forge: bad pull_request_review_comment payload: %w", err)
	}
	if p.Action != "created" || strings.TrimSpace(p.Comment.Body) == "" {
		return nil, false, nil
	}
	line := p.Comment.Line
	if line == 0 {
		line = p.Comment.OriginalLine
	}
	// An inline review comment is a change request in spirit.
	return &PRReview{
		RepoFullName: p.Repository.FullName,
		PRURL:        p.PullRequest.HTMLURL,
		HeadRef:      p.PullRequest.Head.Ref,
		State:        "changes_requested",
		Author:       p.Comment.User.Login,
		Comments: []Comment{{
			Body:   strings.TrimSpace(p.Comment.Body),
			Path:   p.Comment.Path,
			Line:   line,
			Author: p.Comment.User.Login,
		}},
	}, true, nil
}
