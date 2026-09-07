// Pure helpers shared by the kanban and the run detail. Kept free of Vue so they
// are unit-testable on their own.

// KANBAN_COLUMNS is the board layout: every run status maps into exactly one
// column. Order matters — it is the left-to-right order on screen.
export const KANBAN_COLUMNS = [
  { key: 'active', title: 'In progress', statuses: ['queued', 'running'] },
  { key: 'approval', title: 'Awaiting approval', statuses: ['awaiting_approval'] },
  { key: 'attention', title: 'Needs attention', statuses: ['needs_human_review', 'changes_requested'] },
  { key: 'review', title: 'Ready for review', statuses: ['pr_ready', 'pr_open'] },
  { key: 'closed', title: 'Closed', statuses: ['accepted', 'done', 'failed'] },
]

const COLUMN_BY_STATUS = Object.fromEntries(
  KANBAN_COLUMNS.flatMap((c) => c.statuses.map((s) => [s, c.key])),
)

/** columnFor maps a run status to a kanban column key, or null if unknown. */
export function columnFor(status) {
  return COLUMN_BY_STATUS[status] ?? null
}

/** groupByColumn buckets runs into { columnKey: [run, ...] }, newest first. */
export function groupByColumn(runs) {
  const out = Object.fromEntries(KANBAN_COLUMNS.map((c) => [c.key, []]))
  for (const run of runs ?? []) {
    const key = columnFor(run.status)
    if (key) out[key].push(run)
  }
  for (const key of Object.keys(out)) {
    out[key].sort((a, b) => new Date(b.updatedAt ?? 0) - new Date(a.updatedAt ?? 0))
  }
  return out
}

const STATUS_TAG = {
  queued: 'info',
  running: 'primary',
  awaiting_approval: 'warning',
  needs_human_review: 'warning',
  changes_requested: 'warning',
  pr_ready: 'success',
  pr_open: 'success',
  accepted: 'success',
  done: 'info',
  failed: 'danger',
}

/** statusTagType maps a run status to an Element Plus tag type. */
export function statusTagType(status) {
  return STATUS_TAG[status] ?? 'info'
}

/** statusLabel turns snake_case statuses and stages into human text. */
export function statusLabel(s) {
  if (!s) return '—'
  return s.replace(/_/g, ' ')
}

const SEVERITY_TAG = {
  critical: 'danger',
  high: 'danger',
  medium: 'warning',
  low: 'info',
  info: 'info',
}

export function severityTagType(sev) {
  return SEVERITY_TAG[(sev ?? '').toLowerCase()] ?? 'info'
}

const EVENT_TAG = {
  stage_failed: 'danger',
  budget_exhausted: 'danger',
  attempt_cap: 'danger',
  awaiting_approval: 'warning',
  request_changes: 'warning',
  review_changes_requested: 'warning',
  recovered: 'warning',
  rejected: 'warning',
  approved: 'success',
  review_accepted: 'success',
  pr_opened: 'success',
  finished: 'success',
  stage_completed: 'primary',
}

export function eventTagType(kind) {
  return EVENT_TAG[kind] ?? 'info'
}

/** TERMINAL are the statuses a run never leaves on its own (polling can stop). */
export const TERMINAL = new Set(['pr_ready', 'pr_open', 'accepted', 'failed', 'done', 'needs_human_review'])

export function isTerminal(status) {
  return TERMINAL.has(status)
}

/** fmtDuration renders a millisecond duration as "1m 20s" / "820ms". */
export function fmtDuration(ms) {
  if (ms === undefined || ms === null || ms <= 0) return ''
  if (ms < 1000) return `${Math.round(ms)}ms`
  const total = Math.round(ms / 1000)
  const m = Math.floor(total / 60)
  const s = total % 60
  return m > 0 ? `${m}m ${s}s` : `${s}s`
}

/** fmtRelative renders an ISO timestamp as "just now" / "4m ago" / "2h ago". */
export function fmtRelative(iso, now = Date.now()) {
  if (!iso) return ''
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return ''
  const secs = Math.max(0, Math.round((now - then) / 1000))
  if (secs < 10) return 'just now'
  if (secs < 60) return `${secs}s ago`
  const mins = Math.floor(secs / 60)
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(hours / 24)}d ago`
}

/** fmtTime renders an ISO timestamp as a local clock time. */
export function fmtTime(iso) {
  if (!iso) return ''
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString()
}

/** DEVELOPER_ROLES are the roles a human review comment can be routed to. */
export const DEVELOPER_ROLES = ['backend-developer', 'frontend-developer', 'mobile-developer']
