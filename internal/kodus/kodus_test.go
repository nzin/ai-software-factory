package kodus

import "testing"

func TestParseEnvelope(t *testing.T) {
	out := []byte(`some progress line
{"ok":true,"command":"review","data":{"issues":[
  {"file":"main.go","line":10,"severity":"HIGH","category":"bug","title":"nil deref","suggestion":"guard it"},
  {"path":"web/app.vue","startLine":3,"severity":"low","message":"unused var"}
]},"error":null,"meta":{"schemaVersion":"1.0"}}`)

	findings, err := parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %d", len(findings))
	}
	if findings[0].File != "main.go" || findings[0].Line != 10 || findings[0].Severity != "high" {
		t.Fatalf("f0 = %+v", findings[0])
	}
	if findings[1].File != "web/app.vue" || findings[1].Line != 3 || findings[1].Title != "unused var" {
		t.Fatalf("f1 = %+v", findings[1])
	}
}

func TestParseError(t *testing.T) {
	f, err := parse([]byte(`{"ok":false,"data":null,"error":{"message":"rate limited"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != 1 || f[0].Severity != "info" {
		t.Fatalf("got %+v", f)
	}
}

func TestReviewUnconfigured(t *testing.T) {
	t.Setenv("KODUS_TEAM_KEY", "")
	f, err := Review(t.Context(), t.TempDir(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != 1 || f[0].Source != "kodus" {
		t.Fatalf("got %+v", f)
	}
}
