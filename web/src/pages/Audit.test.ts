import { describe, expect, it } from 'vitest'
import type { AuditEntry } from '../types'
import { describeAudit } from './Audit'

const entry = (action: string, data: unknown, targetId: string | null = 'u2'): AuditEntry => ({
  id: 1,
  actorId: 'u1',
  actorName: 'Alice',
  action,
  targetType: 'user',
  targetId,
  data,
  ip: null,
  createdAt: new Date().toISOString(),
})

describe('describeAudit', () => {
  const names = (id: string) => ({ u2: 'Bob' })[id]
  it('names the person a role change was about', () => {
    expect(describeAudit(entry('member.role', { from: 'member', to: 'viewer' }), names)).toBe("changed Bob's workspace role from member to viewer")
  })
  it('copes with people who have since left', () => {
    expect(describeAudit(entry('member.remove', {}, 'gone'), names)).toBe('removed a former member')
  })
  it('falls back to a label, then the raw action', () => {
    expect(describeAudit(entry('webhook.github.rotate', {}), names)).toBe('generated a GitHub webhook secret')
    expect(describeAudit(entry('something.new', {}), names)).toBe('something.new')
  })
})
