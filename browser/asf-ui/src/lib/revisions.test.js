import { describe, expect, it } from 'vitest'
import { revisionsFrom } from './revisions'

const ev = (kind, extra = {}) => ({ kind, at: '2026-01-01T10:00:00Z', ...extra })

describe('revisionsFrom', () => {
  it('returns one open revision for a run still in progress', () => {
    const revs = revisionsFrom([
      ev('submitted'),
      ev('stage_started', { stage: 'planner' }),
      ev('stage_completed', { stage: 'planner' }),
      ev('stage_started', { stage: 'backend-developer' }),
    ])
    expect(revs).toHaveLength(1)
    expect(revs[0].n).toBe(1)
    expect(revs[0].open).toBe(true)
    expect(revs[0].trigger.kind).toBe('initial')
  })

  it('closes a revision on finished + pr_ready', () => {
    const revs = revisionsFrom([
      ev('submitted'),
      ev('stage_completed', { stage: 'backend-developer' }),
      ev('stage_completed', { stage: 'build-gate' }),
      ev('stage_completed', { stage: 'security-reviewer' }),
      ev('finished', { status: 'pr_ready', at: '2026-01-01T10:06:00Z' }),
    ])
    expect(revs).toHaveLength(1)
    expect(revs[0].open).toBe(false)
    expect(revs[0].status).toBe('pr_ready')
    expect(revs[0].steps).toBe(3)
    expect(revs[0].durationMs).toBe(6 * 60 * 1000)
  })

  it('splits into revisions and records what triggered each', () => {
    const revs = revisionsFrom([
      ev('submitted'),
      ev('stage_completed', { stage: 'backend-developer' }),
      ev('finished', { status: 'pr_open' }),
      ev('review_changes_requested', { message: 'human requested changes (1)', actor: 'human' }),
      ev('stage_started', { stage: 'backend-developer' }),
      ev('stage_completed', { stage: 'backend-developer' }),
      ev('finished', { status: 'pr_open' }),
      ev('build_failed', { message: 'build-gate: the build failed' }),
      ev('stage_started', { stage: 'backend-developer' }),
    ])
    expect(revs.map((r) => r.n)).toEqual([1, 2, 3])
    expect(revs[0].trigger.kind).toBe('initial')
    expect(revs[1].trigger.kind).toBe('human review')
    expect(revs[1].trigger.actor).toBe('human')
    expect(revs[2].trigger.kind).toBe('build gate')
    expect(revs[2].open).toBe(true)
  })

  it('handles an empty log', () => {
    const revs = revisionsFrom([])
    expect(revs).toHaveLength(1)
    expect(revs[0].open).toBe(true)
  })
})
