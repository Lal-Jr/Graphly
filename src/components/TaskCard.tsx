import type { Task } from '../lib/types'
import { useBoard, useViewers } from '../store/context'
import { Avatar, nameColor } from './ui'

interface Props {
  task: Task
  related: 'upstream' | 'downstream' | 'self' | null
  onOpen: (id: string) => void
  onHover: (id: string | null) => void
  onDragStart: (id: string) => void
  onDropBefore: (id: string) => void
}

export function TaskCard({ task, related, onOpen, onHover, onDragStart, onDropBefore }: Props) {
  const { analysis } = useBoard()
  const viewers = useViewers(task.id)
  const blockedBy = analysis.blockedBy.get(task.id)?.length ?? 0
  const blocking = analysis.impact.get(task.id) ?? 0
  const critical = analysis.criticalSet.has(task.id)
  const cyclic = analysis.inCycle.has(task.id)
  const atRisk = analysis.atRisk.includes(task.id)

  const classes = ['card', task.status === 'done' && 'done', blockedBy && 'blocked', critical && 'critical', related && `rel-${related}`]
    .filter(Boolean)
    .join(' ')

  return (
    <article
      className={classes}
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData('text/plain', task.id)
        e.dataTransfer.effectAllowed = 'move'
        onDragStart(task.id)
      }}
      onDragOver={(e) => e.preventDefault()}
      onDrop={(e) => {
        e.preventDefault()
        e.stopPropagation()
        onDropBefore(task.id)
      }}
      onClick={() => onOpen(task.id)}
      onMouseEnter={() => onHover(task.id)}
      onMouseLeave={() => onHover(null)}
      onKeyDown={(e) => e.key === 'Enter' && onOpen(task.id)}
      tabIndex={0}
    >
      <div className="card-top">
        <span className={`prio prio-${task.priority}`} title={`${task.priority} priority`} />
        <h3>{task.title || <em>Untitled</em>}</h3>
        {viewers.length > 0 && (
          <span className="viewers">
            {viewers.map((v) => (
              <Avatar key={v.clientId} user={v} size={18} ring />
            ))}
          </span>
        )}
      </div>
      <div className="badges">
        {cyclic && <span className="badge danger">⟳ Cycle</span>}
        {atRisk && <span className="badge danger" title="Work started while a dependency is still open">⚠ At risk</span>}
        {blockedBy > 0 && !atRisk && <span className="badge danger">⛔ Blocked by {blockedBy}</span>}
        {blocking > 0 && <span className="badge warn">⤳ Blocks {blocking}</span>}
        {critical && <span className="badge crit">◆ Critical</span>}
      </div>
      <div className="card-meta">
        <span className="est">{task.estimate}d</span>
        {task.assignee && <Avatar user={{ name: task.assignee, color: nameColor(task.assignee) }} size={20} />}
      </div>
    </article>
  )
}
