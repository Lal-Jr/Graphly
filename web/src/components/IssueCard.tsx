import { memo } from 'react'
import { formatDate } from '../lib/forecast'
import { useSession } from '../store/session'
import { useProject, useViewers } from '../store/useProject'
import type { Issue } from '../types'
import { Icon, PriorityIcon, TypeIcon } from '../ui/icons'
import { Avatar, AvatarStack } from '../ui/primitives'

export type Relation = 'self' | 'upstream' | 'downstream'

interface Props {
  issue: Issue
  relation?: Relation
  onOpen: (key: string) => void
  onHover?: (id: string | null) => void
  onDragStart?: (id: string) => void
  onDropBefore?: (id: string) => void
}

/**
 * A card is a node in the dependency graph, so it's drawn like one: a port on the left edge when it
 * waits on open work, a port on the right when others wait on it, and its slack in the footer.
 */
export const IssueCard = memo(function IssueCard({ issue, relation, onOpen, onHover, onDragStart, onDropBefore }: Props) {
  const d = useProject()
  const { user } = useSession()
  const viewers = useViewers(issue.id, user?.id)
  const key = d.keyOf(issue)
  const blockers = d.analysis.blockedBy.get(issue.id) ?? []
  const impact = d.analysis.impact.get(issue.id) ?? 0
  const critical = d.analysis.criticalSet.has(issue.id)
  const atRisk = d.analysis.atRisk.includes(issue.id)
  const slack = d.analysis.schedule.get(issue.id)?.slack
  const fc = d.forecasts.get(issue.id)
  const done = d.category(issue) === 'done'
  const epic = issue.epicId ? d.issueMap.get(issue.epicId) : null
  const assignee = issue.assigneeId ? d.memberMap.get(issue.assigneeId) : null
  const blockerKeys = blockers.map((b) => d.keyOf(d.issueMap.get(b)!))

  const cls = ['issue-card', done && 'is-done', blockers.length > 0 && 'is-blocked', critical && 'is-critical', relation && `rel-${relation}`]
    .filter(Boolean)
    .join(' ')

  return (
    <article
      className={cls}
      draggable={!!onDragStart}
      tabIndex={0}
      data-issue={issue.id}
      onDragStart={(e) => {
        e.dataTransfer.setData('text/plain', issue.id)
        e.dataTransfer.effectAllowed = 'move'
        onDragStart?.(issue.id)
      }}
      onDragOver={(e) => onDropBefore && e.preventDefault()}
      onDrop={(e) => {
        if (!onDropBefore) return
        e.preventDefault()
        e.stopPropagation()
        onDropBefore(issue.id)
      }}
      onClick={() => onOpen(key)}
      onKeyDown={(e) => e.key === 'Enter' && onOpen(key)}
      onMouseEnter={() => onHover?.(issue.id)}
      onMouseLeave={() => onHover?.(null)}
    >
      {!done && blockers.length > 0 && (
        <span className="port port-in" title={`Waiting on ${blockerKeys.join(', ')}`}>
          {blockers.length}
        </span>
      )}
      {!done && impact > 0 && (
        <span className="port port-out" title={`${impact} open issues wait on this`}>
          {impact}
        </span>
      )}
      {viewers.length > 0 && (
        <span className="card-viewers" title={`${viewers.map((v) => v.name).join(', ')} viewing`}>
          <AvatarStack users={viewers.map((v) => ({ ...v, key: v.userId }))} size={18} max={3} />
        </span>
      )}

      <header className="card-head">
        <TypeIcon type={issue.type} size={15} />
        <span className={`issue-key${done ? ' done' : ''}`}>{key}</span>
        <span className="spacer" />
        <PriorityIcon priority={issue.priority} size={15} />
      </header>

      <p className="card-title">{issue.title}</p>

      {!done && (atRisk || blockers.length > 0 || (fc?.daysLate ?? 0) > 0) && (
        <div className="card-signals">
          {blockers.length > 0 && (
            <span className={`signal ${atRisk ? 'signal-danger' : 'signal-wait'}`} title={atRisk ? 'Started while a blocker is still open' : `Waiting on ${blockerKeys.join(', ')}`}>
              <Icon name={atRisk ? 'warning' : 'block'} size={12} />
              {atRisk ? 'At risk · ' : 'Waiting on '}
              <span className="mono">{blockerKeys[0]}</span>
              {blockerKeys.length > 1 && ` +${blockerKeys.length - 1}`}
            </span>
          )}
          {fc && fc.daysLate > 0 && (
            <span className="signal signal-danger" title={`Forecast to finish ${formatDate(fc.finish)}, due ${formatDate(issue.dueDate!)}`}>
              <Icon name="clock" size={12} /> {fc.daysLate}d past due
            </span>
          )}
        </div>
      )}

      {(epic || issue.labels.length > 0) && (
        <div className="card-tags">
          {epic && (
            <span className="epic-chip" title={`Epic: ${epic.title}`}>
              {epic.title}
            </span>
          )}
          {issue.labels.slice(0, 3).map((l) => (
            <span key={l} className="label-chip">
              {l}
            </span>
          ))}
          {issue.labels.length > 3 && <span className="label-chip">+{issue.labels.length - 3}</span>}
        </div>
      )}

      <footer className="card-footer">
        {!done && slack !== undefined && (
          <span className={`slack${critical ? ' is-zero' : ''}`} title={critical ? 'On the critical path: any delay moves the ship date' : `Can slip ${slack} working days without moving the ship date`}>
            {critical ? (
              <>
                <Icon name="flame" size={12} /> critical
              </>
            ) : (
              `${slack}d slack`
            )}
          </span>
        )}
        {issue.dueDate && !done && (
          <span className={`card-due${fc && fc.daysLate > 0 ? ' late' : ''}`} title="Due date">
            <Icon name="calendar" size={12} />
            {formatDate(issue.dueDate)}
          </span>
        )}
        <span className="spacer" />
        {impact > 0 && !done && (
          <span className="unblocks" title={`Finishing this unblocks ${impact} issues`}>
            <Icon name="zap" size={12} />
            {impact}
          </span>
        )}
        <span className="estimate-pill" title="Estimate (days)">
          {issue.estimate}d
        </span>
        <Avatar user={assignee} size={22} />
      </footer>
    </article>
  )
})
