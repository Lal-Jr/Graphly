import { useEffect, useState } from 'react'
import { wouldCreateCycle } from '../lib/graph'
import { COLUMNS, PRIORITIES, type Status, type Priority } from '../lib/types'
import { useBoard, useViewers } from '../store/context'
import { Avatar } from './ui'

export function TaskEditor({ id, onClose, onOpen }: { id: string; onClose: () => void; onOpen: (id: string) => void }) {
  const { board, tasks, byId, analysis } = useBoard()
  const viewers = useViewers(id)
  const task = byId.get(id)
  const [confirmDelete, setConfirmDelete] = useState(false)

  useEffect(() => {
    board.setViewing(id)
    return () => board.setViewing(undefined)
  }, [board, id])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  // Someone else deleted it while we had it open.
  useEffect(() => {
    if (!task) onClose()
  }, [task, onClose])
  if (!task) return null

  const deps = task.dependsOn.filter((d) => byId.has(d))
  const dependents = tasks.filter((t) => t.dependsOn.includes(id))
  const candidates = tasks.filter((t) => t.id !== id && !deps.includes(t.id) && !wouldCreateCycle(tasks, id, t.id))
  const sched = analysis.schedule.get(id)
  const openDeps = analysis.blockedBy.get(id) ?? []

  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-label="Edit task">
        <div className="modal-head">
          <input
            className="title-input"
            value={task.title}
            onChange={(e) => board.updateTask(id, { title: e.target.value })}
            placeholder="Task title"
            autoFocus={!task.title}
          />
          {viewers.length > 0 && (
            <span className="viewers" title="Also viewing">
              {viewers.map((v) => (
                <Avatar key={v.clientId} user={v} ring />
              ))}
            </span>
          )}
          <button className="icon" onClick={onClose} aria-label="Close">
            ✕
          </button>
        </div>

        {(openDeps.length > 0 || analysis.inCycle.has(id) || analysis.criticalSet.has(id)) && (
          <div className="flags">
            {analysis.inCycle.has(id) && <span className="badge danger">⟳ Part of a dependency cycle — remove one of its links</span>}
            {openDeps.length > 0 && <span className="badge danger">⛔ Waiting on {openDeps.length} open {openDeps.length === 1 ? 'task' : 'tasks'}</span>}
            {analysis.criticalSet.has(id) && <span className="badge crit">◆ On the critical path — any delay slips the project</span>}
          </div>
        )}

        <div className="fields">
          <label>
            Status
            <select value={task.status} onChange={(e) => board.updateTask(id, { status: e.target.value as Status })}>
              {COLUMNS.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.label}
                </option>
              ))}
            </select>
          </label>
          <label>
            Priority
            <select value={task.priority} onChange={(e) => board.updateTask(id, { priority: e.target.value as Priority })}>
              {PRIORITIES.map((p) => (
                <option key={p} value={p}>
                  {p[0].toUpperCase() + p.slice(1)}
                </option>
              ))}
            </select>
          </label>
          <label>
            Estimate (days)
            <input
              type="number"
              min={0}
              step={0.5}
              value={task.estimate}
              onChange={(e) => board.updateTask(id, { estimate: Math.max(0, Number(e.target.value) || 0) })}
            />
          </label>
          <label>
            Assignee
            <input value={task.assignee} onChange={(e) => board.updateTask(id, { assignee: e.target.value })} placeholder="Unassigned" />
          </label>
        </div>

        <label className="block">
          Description
          <textarea
            rows={4}
            value={task.description}
            onChange={(e) => board.updateTask(id, { description: e.target.value })}
            placeholder="Add details…"
          />
        </label>

        <div className="relations">
          <div>
            <h4>Depends on</h4>
            <ul>
              {deps.map((d) => {
                const t = byId.get(d)!
                return (
                  <li key={d}>
                    <button className="link" onClick={() => onOpen(d)}>
                      <span className={`dot s-${t.status}`} />
                      {t.title || 'Untitled'}
                    </button>
                    <button className="icon" onClick={() => board.removeDependency(id, d)} aria-label="Remove dependency">
                      ✕
                    </button>
                  </li>
                )
              })}
              {deps.length === 0 && <li className="muted">Nothing — ready whenever you are</li>}
            </ul>
            {candidates.length > 0 && (
              <select
                value=""
                onChange={(e) => e.target.value && board.addDependency(id, e.target.value)}
                aria-label="Add dependency"
              >
                <option value="">+ Add dependency…</option>
                {candidates.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.title || 'Untitled'}
                  </option>
                ))}
              </select>
            )}
          </div>
          <div>
            <h4>Blocks</h4>
            <ul>
              {dependents.map((t) => (
                <li key={t.id}>
                  <button className="link" onClick={() => onOpen(t.id)}>
                    <span className={`dot s-${t.status}`} />
                    {t.title || 'Untitled'}
                  </button>
                </li>
              ))}
              {dependents.length === 0 && <li className="muted">Nothing depends on this</li>}
            </ul>
          </div>
        </div>

        {sched && (
          <div className="schedule">
            <span>
              Earliest start <b>day {sched.earliestStart}</b>
            </span>
            <span>
              Earliest finish <b>day {sched.earliestFinish}</b>
            </span>
            <span>
              Slack <b>{sched.slack}d</b>
            </span>
          </div>
        )}

        <div className="modal-foot">
          {confirmDelete ? (
            <>
              <span className="muted">Delete for everyone?</span>
              <button className="danger" onClick={() => board.deleteTask(id)}>
                Yes, delete
              </button>
              <button className="ghost" onClick={() => setConfirmDelete(false)}>
                Cancel
              </button>
            </>
          ) : (
            <button className="ghost danger-text" onClick={() => setConfirmDelete(true)}>
              Delete task
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
