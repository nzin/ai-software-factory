// revisionsFrom derives the PR revision history from a run's flat event log.
//
// A revision is the work between one "branch ready" point and the next: it ends
// on a `finished` event whose status is pr_ready / pr_open. Revision 1 is the
// initial pass; each later revision is triggered by whatever bounced the run
// back — a reviewer, the build gate, or a human review (in the UI or on GitHub).
// A trailing partial (the run is still working, or ended without a PR) is marked
// `open`.

const READY = new Set(['pr_ready', 'pr_open'])

const TRIGGER_KINDS = {
  request_changes: 'reviewer',
  build_failed: 'build gate',
  review_changes_requested: 'human review',
  attempt_cap: 'retry cap',
  resumed: 'resumed',
}

const DEFAULT_TRIGGER = { kind: 'reviewer', text: 'sent back for changes' }

export function revisionsFrom(events) {
  const evs = events ?? []
  const revisions = []
  let current = newRevision(1)
  let pendingTrigger = null // set by a bounce, consumed by the next revision

  for (const e of evs) {
    // A trigger event comes *after* the finished event that closed the previous
    // revision, so record it before deciding what lands where.
    if (e.kind in TRIGGER_KINDS) {
      pendingTrigger = {
        kind: TRIGGER_KINDS[e.kind],
        text: e.message || '',
        detail: e.detail || '',
        actor: e.actor || 'system',
      }
    }

    if (current.startedAt == null && e.at) {
      current.startedAt = e.at
      if (current.n > 1) {
        current.trigger = pendingTrigger || DEFAULT_TRIGGER
        pendingTrigger = null
      }
    }

    if (e.kind === 'stage_completed' || e.kind === 'stage_failed') current.steps += 1

    if (e.kind === 'finished' && READY.has(e.status)) {
      current.endedAt = e.at
      current.status = e.status
      current.durationMs = durationMs(current.startedAt, current.endedAt)
      revisions.push(current)
      current = newRevision(revisions.length + 1)
    }
  }

  if (current.startedAt != null || revisions.length === 0) {
    const last = evs[evs.length - 1]
    current.open = true
    current.endedAt = last?.at ?? null
    current.durationMs = durationMs(current.startedAt, current.endedAt)
    if (current.n > 1 && !current.trigger) current.trigger = pendingTrigger || DEFAULT_TRIGGER
    revisions.push(current)
  }

  return revisions
}

function newRevision(n) {
  return {
    n,
    startedAt: null,
    endedAt: null,
    status: null,
    steps: 0,
    durationMs: 0,
    open: false,
    trigger: n === 1 ? { kind: 'initial', text: '' } : null,
  }
}

function durationMs(a, b) {
  if (!a || !b) return 0
  const ms = new Date(b).getTime() - new Date(a).getTime()
  return Number.isFinite(ms) && ms > 0 ? ms : 0
}
