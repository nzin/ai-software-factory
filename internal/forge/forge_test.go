package forge

import (
	"context"
	"testing"
)

func TestGithubSlug(t *testing.T) {
	cases := []struct {
		in          string
		owner, repo string
		ok          bool
	}{
		{"https://github.com/nzin/ai-software-factory", "nzin", "ai-software-factory", true},
		{"https://github.com/nzin/ai-software-factory.git", "nzin", "ai-software-factory", true},
		{"git@github.com:nzin/ai-software-factory.git", "nzin", "ai-software-factory", true},
		{"ssh://git@github.com/acme/widgets.git", "acme", "widgets", true},
		{"https://gitlab.com/nzin/thing.git", "", "", false},
		{"", "", "", false},
		{"/tmp/local/repo", "", "", false},
	}
	for _, c := range cases {
		o, r, ok := githubSlug(c.in)
		if ok != c.ok || o != c.owner || r != c.repo {
			t.Errorf("githubSlug(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, o, r, ok, c.owner, c.repo, c.ok)
		}
	}
}

func TestOpenPRNoTokenNoNetwork(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	url, err := OpenPR(context.Background(), "https://github.com/nzin/x", "main", "asf/run-1", "t", "b")
	if err != nil || url != "" {
		t.Fatalf("no-token OpenPR = (%q, %v), want (\"\", nil)", url, err)
	}
}

func TestOpenPRNonGitHub(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "would-not-be-used")
	url, err := OpenPR(context.Background(), "https://bitbucket.org/a/b", "main", "h", "t", "b")
	if err != nil || url != "" {
		t.Fatalf("non-github OpenPR = (%q, %v), want (\"\", nil)", url, err)
	}
}
