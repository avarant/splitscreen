# Splitscreen

A self-hostable control plane for running coding agents that teams drive from
chat.

One **gateway** owns the chat surfaces and every credential. Many **runners**
own working trees and execute agents, holding nothing secret at rest.

```
   Slack ─┐                                          ┌── Claude Code
  Discord ├──▶ Surface adapters ──▶  GATEWAY  ──▶ Runner ├── (your harness)
    Web ──┘                          (singleton)        └── …
                                         │
                                    ┌────┴────┐
                                    │ SQLite  │  routing · audit · usage
                                    └─────────┘
```

See [DESIGN.md](DESIGN.md) for the architecture and the reasoning behind it.

## Why

A chat-to-agent bridge usually starts as one process: one chat app, one token,
one channel allowlist, one machine, one working tree. Every axis is welded to
every other, so adding an environment means adding all of them — and two
processes sharing one chat app receive each message nondeterministically,
which presents as "the bot is flaky" rather than as a configuration error.

Splitscreen separates the thing that talks to humans from the thing that runs
agents:

- **One chat app serves many environments**, with distinct visible personas.
- **Adding an environment is a config change**, not an install procedure.
- **No long-lived credentials** on machines that execute agent code.
- **Complete audit trail**: every message, tool call, permission decision, file
  transfer, third-party API call, and token spent.

## Install

```sh
go install github.com/avarant/splitscreen/cmd/splitscreen@latest
```

Or build a static binary:

```sh
CGO_ENABLED=0 go build -o splitscreen ./cmd/splitscreen
```

One binary, one subcommand per role. Nothing else to install on a runner host.

## Quick start

**1. Write a config.** See [`examples/splitscreen.yaml`](examples/splitscreen.yaml).

```yaml
gateway:
  listen: 127.0.0.1:8443
  secrets_dir: /etc/splitscreen/secrets
  slack:
    bot_token_secret: slack-bot
    app_token_secret: slack-app

runners:
  staging:
    display: { name: "Clank", icon: ":robot_face:" }
    cwd: /var/www/app
    harness: claude-code
    idle: 30m

routes:
  - { channel: C0123456789, runner: staging }
```

Validate it before anything else runs:

```sh
splitscreen config check -c splitscreen.yaml
```

**2. Generate a certificate and an enrollment token.**

```sh
splitscreen cert --host 10.0.0.5 --cert gateway.crt --key gateway.key
splitscreen enroll staging
```

`cert` prints a fingerprint for runners to pin; `enroll` prints a token and the
secret name the gateway looks it up under.

**3. Run the gateway.**

```sh
splitscreen gateway -c splitscreen.yaml
```

**4. Run a runner**, on any host that can reach the gateway:

```sh
splitscreen runner \
  --name staging \
  --gateway wss://10.0.0.5:8443 \
  --fingerprint sha256:… \
  --token-file /etc/splitscreen/token \
  --cwd /var/www/app
```

Runners dial out. They never listen, so a private subnet, NAT, or a laptop all
work with no inbound rules.

## In chat

Send a message in a routed channel. Unrouted channels are ignored entirely —
this replaces the per-runner allowlists a single-process bridge needs.

| Command | Effect |
|---|---|
| `!new` | Start a fresh session in this thread |
| `!rebind` | Move the thread to the channel's current runner |
| `!runner <name>` | Pick a runner when starting a new thread |
| `!status` | Runner roster, connection state, bundle versions, queue depths |
| `!routes` | The routing table |
| `!cost` | Spend and token usage for the last week |

A read-only web view is served on loopback (`127.0.0.1:8480` by default). Reach
it with a port-forward — `aws ssm start-session`, `ssh -L`, or equivalent — so
authorization stays the platform's problem rather than something this process
has to invent. Pass `--web ""` to disable it.

Threads are sticky: a thread keeps its runner even after a route changes,
because its session lives on that runner's disk.

## Credentials

Everything credential-bearing lives on the gateway. The one exception is the
harness credential, which must exist where the agent process runs.

| Credential | Where it lives |
|---|---|
| Chat platform tokens | Gateway only — runners never call a chat API |
| Git / forge | Gateway mints per operation, scoped to one repository |
| Credentialed MCP (Jira, Linear, …) | Gateway — proxied, so the runner never sees the token |
| Harness (Claude) | Gateway-held, materialized to the runner's tmpfs, never persisted |

### Git

Runners hold no forge credentials. Wire up the helper and the gateway answers
per operation, after checking policy:

```sh
git config --global credential.helper '!splitscreen credential-helper'
git config --global credential.useHttpPath true
```

`useHttpPath` matters: without it git does not tell the helper which repository
is being accessed, and per-repository scoping becomes impossible.

### MCP servers

Servers split by one question: *does it need the runner's filesystem, or does
it need a credential?*

```yaml
mcp:
  fs:                       # needs the filesystem → runs on the runner
    kind: local
    command: /usr/bin/mcp-filesystem
  jira:                     # needs a credential → runs through the gateway
    kind: proxied
    url: https://mcp.atlassian.com/v1/sse
    auth: basic
    user: bot@example.com
    secret: jira-token
    deny: ["deleteIssue"]
```

A proxied server's `deny` list is enforced at the gateway, before the call
leaves it.

## Policy

Deny rules are evaluated **before** a permission prompt is posted, so a denied
tool is never offered to a human and no click can approve it:

```yaml
runners:
  staging:
    policy:
      approvers: [U01ABC, U02DEF]     # who may resolve prompts
      deny:
        - "Bash(git push --force*)"
        - "Bash(terraform apply*)"
      forge:
        repos: ["acme/app"]           # empty means no git credentials at all
```

That inversion is the core security property: file contents and third-party API
responses are untrusted input the agent reads as instructions, so the agent's
judgment cannot be the control.

## Configuration reference

`splitscreen config check` validates everything and reports every problem at
once. A config either loads wholly or not at all — a bad edit never partially
applies, including on `SIGHUP` reload.

Enforced invariants include: one channel maps to exactly one runner, at most one
DM route, bundle `extends` chains are acyclic, proxied MCP servers declare no
command, local ones declare no credential, and unknown keys are errors rather
than silently-ignored typos.

## Operations

```sh
systemctl reload splitscreen-gateway     # SIGHUP: re-read the config
journalctl -u splitscreen-gateway -f     # structured JSON logs
```

Runners run as an unprivileged user (harnesses refuse dangerous permission modes
as root) with the runtime root on tmpfs. One host can serve several runners:
each gets its own config directory, its own unix socket, and its own persona.

```sh
systemctl --user enable --now splitscreen-runner@staging
systemctl --user enable --now splitscreen-runner@review   # same host, second tree
```

## Status

The protocol, configuration, gateway, runner, Claude Code adapter, Slack
surface, and read-only web view are implemented and tested.

Not yet built: per-user OAuth for third-party credentials (service-account
tokens work today), signed cloud instance-identity authentication as an
alternative to enrollment tokens, and additional surface and harness adapters —
both plug points exist, but only one implementation of each ships.

## License

ISC
