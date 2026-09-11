import { describe, expect, it } from 'vitest'
import {
  KANBAN_COLUMNS,
  columnFor,
  fmtDuration,
  fmtRelative,
  groupByColumn,
  isTerminal,
  severityTagType,
  statusTagType,
} from './status'

describe('columnFor', () => {
  it('maps every status the API can return', () => {
    const statuses = [
      'queued', 'running', 'awaiting_approval', 'pr_ready', 'pr_open',
      'accepted', 'changes_requested', 'needs_human_review', 'failed', 'done',
    ]
    for (const s of statuses) {
      expect(columnFor(s), `status ${s} has no column`).not.toBeNull()
    }
  })

  it('puts each status in exactly one column', () => {
    const seen = new Set()
    for (const col of KANBAN_COLUMNS) {
      for (const s of col.statuses) {
        expect(seen.has(s), `${s} appears twice`).toBe(false)
        seen.add(s)
      }
    }
  })

  it('returns null for an unknown status', () => {
    expect(columnFor('banana')).toBeNull()
  })

  it('puts every closed status (delete-eligible) in the closed column', () => {
    expect(columnFor('accepted')).toBe('closed')
    expect(columnFor('done')).toBe('closed')
    expect(columnFor('failed')).toBe('closed')
    expect(columnFor('running')).not.toBe('closed')
  })
})

describe('groupByColumn', () => {
  it('buckets runs and sorts each column newest first', () => {
    const runs = [
      { id: 'a', status: 'running', updatedAt: '2026-01-01T10:00:00Z' },
      { id: 'b', status: 'running', updatedAt: '2026-01-01T12:00:00Z' },
      { id: 'c', status: 'pr_ready', updatedAt: '2026-01-01T11:00:00Z' },
      { id: 'd', status: 'banana', updatedAt: '2026-01-01T11:00:00Z' },
    ]
    const out = groupByColumn(runs)
    expect(out.active.map((r) => r.id)).toEqual(['b', 'a'])
    expect(out.review.map((r) => r.id)).toEqual(['c'])
    expect(out.approval).toEqual([])
    // an unknown status is dropped, not crammed into a column
    expect(Object.values(out).flat().map((r) => r.id)).not.toContain('d')
  })

  it('handles no runs at all', () => {
    const out = groupByColumn(undefined)
    expect(Object.keys(out)).toEqual(KANBAN_COLUMNS.map((c) => c.key))
  })
})

describe('tag types', () => {
  it('flags failures as danger and successes as success', () => {
    expect(statusTagType('failed')).toBe('danger')
    expect(statusTagType('accepted')).toBe('success')
    expect(statusTagType('awaiting_approval')).toBe('warning')
    expect(statusTagType('nonsense')).toBe('info')
    expect(severityTagType('CRITICAL')).toBe('danger')
    expect(severityTagType('low')).toBe('info')
  })
})

describe('isTerminal', () => {
  it('stops polling only for statuses the run never leaves on its own', () => {
    expect(isTerminal('pr_ready')).toBe(true)
    expect(isTerminal('needs_human_review')).toBe(true)
    expect(isTerminal('running')).toBe(false)
    expect(isTerminal('awaiting_approval')).toBe(false)
  })
})

describe('fmtDuration', () => {
  it('formats sub-second, seconds and minutes', () => {
    expect(fmtDuration(0)).toBe('')
    expect(fmtDuration(undefined)).toBe('')
    expect(fmtDuration(820)).toBe('820ms')
    expect(fmtDuration(24_000)).toBe('24s')
    expect(fmtDuration(80_000)).toBe('1m 20s')
  })
})

describe('fmtRelative', () => {
  const now = Date.parse('2026-01-01T12:00:00Z')
  it('renders an age relative to now', () => {
    expect(fmtRelative('2026-01-01T11:59:55Z', now)).toBe('just now')
    expect(fmtRelative('2026-01-01T11:59:30Z', now)).toBe('30s ago')
    expect(fmtRelative('2026-01-01T11:56:00Z', now)).toBe('4m ago')
    expect(fmtRelative('2026-01-01T10:00:00Z', now)).toBe('2h ago')
    expect(fmtRelative('2025-12-30T12:00:00Z', now)).toBe('2d ago')
  })
  it('is blank for missing or unparseable input', () => {
    expect(fmtRelative('')).toBe('')
    expect(fmtRelative('not-a-date')).toBe('')
  })
})
