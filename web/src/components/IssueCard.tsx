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

export const IssueCard = memo(function IssueCard({ issue, relation, onOpen, onHover, onDragStart, onDropBefore }: Props) {
  const d = useProject()
  const { user } = useSession()
  const viewers = useViewers(issue.id, user?.id)
  const key = d.keyOf(issue)
  const openBlockers = d.analysis.blockedBy.get(issue.id)?.length ?? 0
  const impact = d.analysis.impact.get(issue.id) ?? 0
  const critical = d.analysis.criticalSet.has(issue.id)
  const atRisk = d.analysis.atRisk.includes(issue.id)
  const fc = d.forecasts.get(issue.id)
  const done = d.category(issue) === 'done'
  const epic = issue.epicId ? d.issueMap.get(issue.epicId) : null
  const assignee = issue.assigneeId ? d.memberMap.get(issue.assigneeId) : null

  const cls = ['issue-card', done && 'is-done', openBlockers > 0 && 'is-blocked', critical && 'is-critical', relation && `rel-${relation}`]
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
      {viewers.length > 0 && (
        <span className="card-viewers" title={`${viewers.map((v) => v.name).join(', ')} viewing`}>
          <AvatarStack users={viewers.map((v) => ({ ...v, key: v.userId }))} size={18} max={3} />
        </span>
      )}
      <p className="card-title">{issue.title}</p>

      {(epic || issue.labels.length > 0) && (
        <div className="card-tags">
          {epic && <span className="epic-chip">{epic.title}</span>}
          {issue.labels.slice(0, 2).map((l) => (
            <span key={l} className="label-chip">
              {l}
            </span>
          ))}
          {issue.labels.length > 2 && <span className="label-chip">+{issue.labels.length - 2}</span>}
        </div>
      )}

      {!done && (openBlockers > 0 || impact > 0 || critical || (fc?.daysLate ?? 0) > 0) && (
        <div className="card-signals">
          {atRisk ? (
            <span className="signal signal-danger" title="Work has started but a blocker is still open">
              <Icon name="warning" size={12} /> At risk
            </span>
          ) : (
            openBlockers > 0 && (
              <span className="signal signal-danger" title={`Waiting on ${d.analysis.blockedBy.get(issue.id)!.map((b) => d.keyOf(d.issueMap.get(b)!)).join(', ')}`}>
                <Icon name="block" size={12} /> Blocked by {openBlockers}
              </span>
            )
          )}
          {impact > 0 && (
            <span className="signal signal-warning" title={`${impact} open issues are waiting on this`}>
              <Icon name="link" size={12} /> Blocks {impact}
            </span>
          )}
          {critical && (
            <span className="signal signal-critical" title="On the critical path — any delay moves the finish date">
              <Icon name="flame" size={12} /> Critical
            </span>
          )}
          {fc && fc.daysLate > 0 && (
            <span className="signal signal-danger" title={`Forecast to finish ${formatDate(fc.finish)}, due ${formatDate(issue.dueDate!)}`}>
              <Icon name="clock" size={12} /> {fc.daysLate}d late
            </span>
          )}
        </div>
      )}

      <footer className="card-footer">
        <TypeIcon type={issue.type} />
        <span className={`issue-key${done ? ' done' : ''}`}>{key}</span>
        <span className="spacer" />
        {issue.dueDate && !done && (
          <span className={`card-due${fc && fc.daysLate > 0 ? ' late' : ''}`} title="Due date">
            <Icon name="calendar" size={12} />
            {formatDate(issue.dueDate)}
          </span>
        )}
        <span className="estimate-pill" title="Estimate (days)">
          {issue.estimate}
        </span>
        <PriorityIcon priority={issue.priority} />
        <Avatar user={assignee} size={24} />
      </footer>
    </article>
  )
})
