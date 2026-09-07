// Package forge opens a pull request on the git host a remote repo lives on.
// GitHub only for now, via the REST API and GITHUB_TOKEN. It never fails a run:
// with no token, a non-GitHub host, or an API error it returns ("", nil) and the
// caller keeps the pushed branch.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// OpenPR opens a PR from head into base on the host of remoteURL. Returns the PR
// URL, or "" when it could not (no token, unsupported host, API error).
func OpenPR(ctx context.Context, remoteURL, base, head, title, body string) (string, error) {
	owner, repo, ok := githubSlug(remoteURL)
	if !ok {
		return "", nil
	}
	token := firstEnv("GITHUB_TOKEN", "GH_TOKEN")
	if token == "" {
		log.Printf("forge: no GITHUB_TOKEN — branch %q pushed, PR not opened", head)
		return "", nil
	}

	payload, _ := json.Marshal(map[string]string{
		"title": title, "head": head, "base": base, "body": body,
	})
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls", owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		log.Printf("forge: github PR request failed: %v", err)
		return "", nil
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusCreated {
		var pr struct {
			HTMLURL string `json:"html_url"`
		}
		_ = json.Unmarshal(rb, &pr)
		return pr.HTMLURL, nil
	}
	log.Printf("forge: github PR not created (%s): %s", resp.Status, truncate(string(rb), 300))
	return "", nil
}

var ghSlug = regexp.MustCompile(`github\.com[/:]([^/]+)/(.+?)(?:\.git)?/?$`)

// githubSlug extracts owner/repo from an https or scp-style github remote.
func githubSlug(remote string) (owner, repo string, ok bool) {
	m := ghSlug.FindStringSubmatch(strings.TrimSpace(remote))
	if len(m) != 3 {
		return "", "", false
	}
	return m[1], strings.TrimSuffix(m[2], "/"), true
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
