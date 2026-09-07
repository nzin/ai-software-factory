# GitHub PR-review webhook

When a run targets a `https://github.com/...` repo and `GITHUB_TOKEN` is set, the
coordinator opens a pull request. This webhook lets a review left **on that PR**
re-enter the factory the same way an in-UI review does:

- a **Request changes** review, or an inline review comment → each comment
  becomes a `Finding{source: "human"}` routed to the responsible developer; the
  run goes back to `running` and produces a new revision on the same branch.
- an **Approve** review → the run is `accepted` (terminal).

Endpoint: `POST /v1/webhooks/github` on the coordinator (`:8090`). It verifies
`X-Hub-Signature-256` (HMAC-SHA256 of the raw body) against
`GITHUB_WEBHOOK_SECRET`. Without that env var the endpoint returns `503`.

## 1. Set the secret

```bash
# in .env
GITHUB_WEBHOOK_SECRET=$(openssl rand -hex 20)
```

`make up` (or restarting `coordinator serve`) picks it up.

## 2a. Local development — `gh webhook forward`

The coordinator must be reachable from GitHub. For local work, tunnel with the
`gh` CLI's webhook extension (`gh extension install cli/gh-webhook`):

```bash
gh webhook forward \
  --repo <owner>/<repo> \
  --events pull_request_review,pull_request_review_comment \
  --url http://localhost:8090/v1/webhooks/github \
  --secret "$GITHUB_WEBHOOK_SECRET"
```

`cloudflared tunnel --url http://localhost:8090` or `ngrok http 8090` work too —
then register the tunnel URL as a real webhook (step 2b).

## 2b. A real webhook

Repo → **Settings → Webhooks → Add webhook**:

- **Payload URL:** `https://<your-host>/v1/webhooks/github`
- **Content type:** `application/json` (required — the endpoint rejects
  `x-www-form-urlencoded`)
- **Secret:** the same `GITHUB_WEBHOOK_SECRET`
- **Events:** "Let me select individual events" → **Pull request reviews** and
  **Pull request review comments**

## Behaviour notes

- The event is matched to a run by the PR URL, falling back to the branch name
  (`asf/run-…`) scoped to the same repo. An event for an unknown or already-closed
  run is logged and answered `202` (so GitHub does not retry).
- Only reviews and inline review comments are handled. Plain PR conversation
  comments (`issue_comment`) are ignored for now.
- Each round still counts against the per-developer attempt cap; too many rounds
  on one role parks the run in `needs_human_review`.
