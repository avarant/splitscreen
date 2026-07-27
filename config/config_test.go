package config

import (
	"strings"
	"testing"
	"time"
)

const valid = `
runners:
  staging:
    display:  { name: "Clank", icon: ":robot_face:" }
    host:     i-06141ec8f53774665
    cwd:      /var/www/ksdm.dev
    harness:  claude-code
    bundle:   staging
    idle:     30m
    policy:
      approvers: [U01ABC, U02DEF]
      deny: ["Bash(git push --force*)"]
      forge:
        repos: ["acme/widgets"]
  dev3-react:
    display:  { name: "Clank React", icon: ":atom_symbol:" }
    cwd:      /var/www/ksdm.dev3
    harness:  claude-code
    bundle:   dev3-react
    idle:     10m

bundles:
  base:
    memory: [org/conventions.md]
    skills: [org/deploy-check]
  staging:
    extends: base
    memory: [runners/staging.md]
  dev3-react:
    extends: base
    memory: [runners/dev3-react.md]
    mcp: [github, jira]

mcp:
  github:
    kind: local
    command: /usr/bin/mcp-github
  jira:
    kind: proxied
    url: https://mcp.example.com/v1
    auth: basic
    user: bot@example.com
    secret: jira-token
    deny: ["deleteIssue"]

routes:
  - channel: C0BK7NB65T4
    runner:  dev3-react
  - channel: C0AXXXXXXX
    runner:  staging
  - dm: true
    runner: staging
`

func TestParseValid(t *testing.T) {
	c, err := Parse([]byte(valid))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := c.Runners["dev3-react"].Idle.Duration(); got != 10*time.Minute {
		t.Errorf("idle = %v, want 10m", got)
	}
	if r, ok := c.RunnerFor("C0BK7NB65T4", false); !ok || r != "dev3-react" {
		t.Errorf("RunnerFor(react channel) = %q,%v", r, ok)
	}
	if r, ok := c.RunnerFor("", true); !ok || r != "staging" {
		t.Errorf("RunnerFor(dm) = %q,%v", r, ok)
	}
	// Unrouted channels are ignored — this replaces per-runner allowlists.
	if _, ok := c.RunnerFor("C0UNKNOWN", false); ok {
		t.Error("an unrouted channel resolved to a runner")
	}
}

func TestBundleResolutionIsBaseFirst(t *testing.T) {
	c, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.ResolveBundle("dev3-react")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"org/conventions.md", "runners/dev3-react.md"}
	if len(got.Memory) != len(want) {
		t.Fatalf("memory = %v, want %v", got.Memory, want)
	}
	for i := range want {
		if got.Memory[i] != want[i] {
			t.Fatalf("memory = %v, want %v", got.Memory, want)
		}
	}
	if len(got.Skills) != 1 || got.Skills[0] != "org/deploy-check" {
		t.Errorf("skills = %v, want the base's", got.Skills)
	}
}

func TestDefaultIdleApplied(t *testing.T) {
	src := `
runners:
  solo:
    display: { name: "Clank" }
    cwd: /srv/app
    harness: claude-code
routes:
  - dm: true
    runner: solo
`
	c, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Runners["solo"].Idle.Duration(); got != DefaultIdle {
		t.Errorf("idle = %v, want default %v", got, DefaultIdle)
	}
}

func TestValidationRejects(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "duplicate channel claim",
			src: `
runners:
  a: { display: {name: A}, cwd: /a, harness: h }
  b: { display: {name: B}, cwd: /b, harness: h }
routes:
  - { channel: C1, runner: a }
  - { channel: C1, runner: b }
`,
			want: "claimed by route 0 and route 1",
		},
		{
			name: "unknown runner in route",
			src: `
runners:
  a: { display: {name: A}, cwd: /a, harness: h }
routes:
  - { channel: C1, runner: a }
  - { channel: C2, runner: ghost }
`,
			want: `unknown runner "ghost"`,
		},
		{
			name: "unknown bundle reference",
			src: `
runners:
  a: { display: {name: A}, cwd: /a, harness: h, bundle: nope }
routes:
  - { channel: C1, runner: a }
`,
			want: `unknown bundle "nope"`,
		},
		{
			name: "bundle extends cycle",
			src: `
runners:
  a: { display: {name: A}, cwd: /a, harness: h, bundle: x }
bundles:
  x: { extends: y }
  y: { extends: x }
routes:
  - { channel: C1, runner: a }
`,
			want: "circular extends",
		},
		{
			name: "two dm routes",
			src: `
runners:
  a: { display: {name: A}, cwd: /a, harness: h }
  b: { display: {name: B}, cwd: /b, harness: h }
routes:
  - { dm: true, runner: a }
  - { dm: true, runner: b }
`,
			want: "second dm route",
		},
		{
			name: "route with both channel and dm",
			src: `
runners:
  a: { display: {name: A}, cwd: /a, harness: h }
routes:
  - { channel: C1, dm: true, runner: a }
`,
			want: "exactly one of channel or dm",
		},
		{
			name: "relative cwd",
			src: `
runners:
  a: { display: {name: A}, cwd: relative/path, harness: h }
routes:
  - { channel: C1, runner: a }
`,
			want: "must be an absolute path",
		},
		{
			name: "missing harness",
			src: `
runners:
  a: { display: {name: A}, cwd: /a }
routes:
  - { channel: C1, runner: a }
`,
			want: "harness is required",
		},
		{
			name: "missing display name",
			src: `
runners:
  a: { cwd: /a, harness: h }
routes:
  - { channel: C1, runner: a }
`,
			want: "display.name is required",
		},
		{
			name: "runner name is not a slug",
			src: `
runners:
  "Dev3 React": { display: {name: A}, cwd: /a, harness: h }
routes:
  - { channel: C1, runner: "Dev3 React" }
`,
			want: "must be a lowercase slug",
		},
		{
			name: "unrouted runner",
			src: `
runners:
  a: { display: {name: A}, cwd: /a, harness: h }
  orphan: { display: {name: O}, cwd: /o, harness: h }
routes:
  - { channel: C1, runner: a }
`,
			want: `runner "orphan" has no routes`,
		},
		{
			name: "malformed forge repo",
			src: `
runners:
  a:
    display: {name: A}
    cwd: /a
    harness: h
    policy: { forge: { repos: ["justaname"] } }
routes:
  - { channel: C1, runner: a }
`,
			want: "owner/name form",
		},
		{
			name: "no runners at all",
			src:  `routes: []`,
			want: "no runners are defined",
		},
		{
			name: "typo'd key is not silently ignored",
			src: `
runners:
  a: { display: {name: A}, cwd: /a, harness: h, idel: 5m }
routes:
  - { channel: C1, runner: a }
`,
			want: "idel",
		},
		{
			name: "bad duration",
			src: `
runners:
  a: { display: {name: A}, cwd: /a, harness: h, idle: "half an hour" }
routes:
  - { channel: C1, runner: a }
`,
			want: "invalid duration",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src))
			if err == nil {
				t.Fatal("expected rejection, got a valid config")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// A bad edit must not partially apply: Parse either returns a usable config or
// an error listing everything wrong with it.
func TestValidationReportsAllProblems(t *testing.T) {
	src := `
runners:
  a: { display: {name: A}, cwd: relative, harness: "" }
routes:
  - { channel: C1, runner: ghost }
  - { channel: C1, runner: ghost }
`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("expected rejection")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("error type = %T, want *ValidationError", err)
	}
	if len(ve.Problems) < 4 {
		t.Errorf("reported %d problems, expected all of them:\n%s", len(ve.Problems), err)
	}
	for _, want := range []string{"absolute path", "harness is required", "unknown runner", "claimed by route"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing problem %q in:\n%s", want, err)
		}
	}
}

func TestMCPValidation(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "credentialed server declared as local",
			src: `
runners:
  a: { display: {name: A}, cwd: /a, harness: h }
mcp:
  jira: { kind: local, command: /bin/x, secret: jira-token }
routes:
  - { channel: C1, runner: a }
`,
			want: "must not declare url or secret",
		},
		{
			name: "proxied server declaring a command",
			src: `
runners:
  a: { display: {name: A}, cwd: /a, harness: h }
mcp:
  jira: { kind: proxied, url: https://x, command: /bin/x }
routes:
  - { channel: C1, runner: a }
`,
			want: "the runner never executes them",
		},
		{
			name: "basic auth without a user",
			src: `
runners:
  a: { display: {name: A}, cwd: /a, harness: h }
mcp:
  jira: { kind: proxied, url: https://x, auth: basic, secret: s }
routes:
  - { channel: C1, runner: a }
`,
			want: "basic auth requires user and secret",
		},
		{
			name: "bundle referencing an undeclared server",
			src: `
runners:
  a: { display: {name: A}, cwd: /a, harness: h, bundle: b }
bundles:
  b: { mcp: [ghost] }
routes:
  - { channel: C1, runner: a }
`,
			want: `unknown mcp server "ghost"`,
		},
		{
			name: "incomplete github app forge",
			src: `
gateway:
  forge: { kind: github-app, app_id: "123" }
runners:
  a: { display: {name: A}, cwd: /a, harness: h }
routes:
  - { channel: C1, runner: a }
`,
			want: "requires app_id, installation_id, and key_file",
		},
		{
			name: "half-configured tls",
			src: `
gateway:
  tls: { cert: /etc/cert.pem }
runners:
  a: { display: {name: A}, cwd: /a, harness: h }
routes:
  - { channel: C1, runner: a }
`,
			want: "cert and key must be set together",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src))
			if err == nil {
				t.Fatal("expected rejection")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestSecretRefsAndDefaults(t *testing.T) {
	c, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if c.Gateway.Listen != DefaultListen {
		t.Errorf("listen = %q, want default", c.Gateway.Listen)
	}
	// A runner with no explicit token_secret still has a resolvable name, so
	// startup can verify every secret exists before serving traffic.
	refs := map[string]bool{}
	for _, r := range c.SecretRefs() {
		refs[r] = true
	}
	for _, want := range []string{"runner-staging", "runner-dev3-react", "jira-token"} {
		if !refs[want] {
			t.Errorf("SecretRefs missing %q (got %v)", want, c.SecretRefs())
		}
	}
	if got := c.ProxiedServers(); len(got) != 1 || got[0] != "jira" {
		t.Errorf("ProxiedServers = %v, want [jira]", got)
	}
}
