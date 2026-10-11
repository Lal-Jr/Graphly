import { describe as suite, expect, it } from 'vitest'
import { describe } from './Inbox'
import type { AppNotification } from '../types'

const n = (kind: AppNotification['kind'], data: Record<string, unknown>): AppNotification => ({
  id: 1, kind, projectId: 'p', issueId: null, actorId: null, data: { projectKey: 'APL', projectName: 'Apollo', ...data }, createdAt: '', readAt: null,
})

suite('notification text', () => {
  it('reads as a sentence for each kind', () => {
    expect(describe(n('unblocked', { key: 'APL-4', byNumber: 3, byTitle: 'Schema' }))).toBe('APL-4 is unblocked: APL-3 Schema is done')
    expect(describe(n('mention', { key: 'APL-4', actorName: 'Ana', in: 'comment' }))).toBe('Ana mentioned you on APL-4')
    expect(describe(n('status', { key: 'APL-4', actorName: 'Ana', from: 'To Do', to: 'Done' }))).toBe('Ana moved APL-4 from To Do to Done')
    expect(describe(n('late', { issues: [{ number: 9, daysLate: 2 }] }))).toBe('APL-9 is now forecast 2d past its due date')
    expect(describe(n('late', { issues: [{ number: 9, daysLate: 2 }, { number: 10, daysLate: 1 }] }))).toBe('2 of your issues are now forecast past their due dates')
    expect(describe(n('status', { key: 'APL-4', from: 'Review', to: 'Done' }))).toBe('Someone moved APL-4 from Review to Done')
  })
})
