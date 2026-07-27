package gateway

import (
	"encoding/json"
	"testing"
)

func TestMatchDeny(t *testing.T) {
	rules := []string{
		"Bash(git push --force*)",
		"Bash(rm -rf /*)",
		"WebFetch",
		"Edit(/etc/*)",
	}

	tests := []struct {
		name  string
		tool  string
		input string
		deny  bool
	}{
		{"force push denied", "Bash", `{"command":"git push --force origin main"}`, true},
		{"ordinary push allowed", "Bash", `{"command":"git push origin main"}`, false},
		{"whole tool denied", "WebFetch", `{"url":"https://example.com"}`, true},
		{"path rule denied", "Edit", `{"file_path":"/etc/passwd"}`, true},
		{"path rule not matched", "Edit", `{"file_path":"/srv/app/main.go"}`, false},
		{"unrelated tool allowed", "Read", `{"file_path":"/etc/passwd"}`, false},
		{"rm -rf denied", "Bash", `{"command":"rm -rf /var/www"}`, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rule, denied := MatchDeny(rules, tc.tool, json.RawMessage(tc.input))
			if denied != tc.deny {
				t.Fatalf("denied = %v (rule %q), want %v", denied, rule, tc.deny)
			}
		})
	}
}

// A rule must not be evadable by burying the argument somewhere the matcher
// does not look; when no recognized field exists, the whole payload is matched
// so a rule still has something to bite on.
func TestSalientArgFallsBackToWholePayload(t *testing.T) {
	rules := []string{"Weird(*dangerous*)"}
	if _, denied := MatchDeny(rules, "Weird", json.RawMessage(`{"unexpected":"dangerous thing"}`)); !denied {
		t.Fatal("expected the rule to match against the whole payload")
	}
}

func TestMatchDenyEmptyRules(t *testing.T) {
	if _, denied := MatchDeny(nil, "Bash", json.RawMessage(`{"command":"anything"}`)); denied {
		t.Fatal("no rules must mean no denial")
	}
}

func TestRepoAllowed(t *testing.T) {
	tests := []struct {
		allowed []string
		repo    string
		want    bool
	}{
		{[]string{"acme/widgets"}, "acme/widgets", true},
		{[]string{"acme/widgets"}, "acme/other", false},
		{[]string{"acme/*"}, "acme/anything", true},
		{[]string{"acme/*"}, "evil/anything", false},
		{[]string{"Acme/Widgets"}, "acme/widgets", true}, // forge names are case-insensitive
		// An empty allowlist denies everything: a runner with no declared
		// repositories has no business minting git credentials.
		{nil, "acme/widgets", false},
		{[]string{}, "acme/widgets", false},
	}
	for _, tc := range tests {
		if got := RepoAllowed(tc.allowed, tc.repo); got != tc.want {
			t.Errorf("RepoAllowed(%v, %q) = %v, want %v", tc.allowed, tc.repo, got, tc.want)
		}
	}
}

func TestSummarizeInput(t *testing.T) {
	if got := SummarizeInput(json.RawMessage(`{"command":"ls -la"}`)); got != "ls -la" {
		t.Errorf("summary = %q", got)
	}
	if got := SummarizeInput(nil); got != "" {
		t.Errorf("summary of nothing = %q, want empty", got)
	}
}
