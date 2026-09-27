import { useState } from 'react'
import { wouldCreateCycle } from '../lib/graph'
import { useIssueParam } from '../store/nav'
import { useSession } from '../store/session'
import { useProject } from '../store/useProject'
import { type Issue, type IssueType, type Priority } from '../types'
import { Icon, TypeIcon } from '../ui/icons'
import { Button, Modal } from '../ui/primitives'
import { useToast } from '../ui/toast'
import { AssigneePicker, EpicPicker, IssuePicker, LabelsEditor, PriorityPicker, TypePicker } from './fields'

export function CreateIssueDialog({ onClose, defaults }: { onClose: () => void; defaults?: Partial<Issue> }) {
  const d = useProject()
  const { user } = useSession()
  const toast = useToast()
  const [, openIssue] = useIssueParam()
  const [type, setType] = useState<IssueType>(defaults?.type ?? 'task')
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [statusId, setStatusId] = useState(defaults?.statusId ?? d.statuses.find((s) => s.category === 'todo')?.id ?? d.statuses[0]?.id)
  const [assigneeId, setAssigneeId] = useState<string | null>(defaults?.assigneeId ?? null)
  const [priority, setPriority] = useState<Priority>('medium')
  const [estimate, setEstimate] = useState('1')
  const [dueDate, setDueDate] = useState('')
  const [labels, setLabels] = useState<string[]>([])
  const [epicId, setEpicId] = useState<string | null>(defaults?.epicId ?? null)
  const [blockers, setBlockers] = useState<string[]>([])
  const [another, setAnother] = useState(false)
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    if (!title.trim()) return
    setBusy(true)
    const issue = await d.store.createIssue(
      {
        type,
        title: title.trim(),
        description,
        statusId,
        assigneeId,
        priority,
        estimate: type === 'epic' ? 0 : Math.max(0, Number(estimate) || 0),
        dueDate: dueDate || null,
        labels,
        epicId: type === 'epic' ? null : epicId,
      },
      type === 'epic' ? [] : blockers,
    )
    setBusy(false)
    if (!issue) return
    toast(`Created ${d.keyOf(issue)}`, 'success')
    if (another) {
      setTitle('')
      setDescription('')
      setBlockers([])
    } else {
      onClose()
      openIssue(d.keyOf(issue))
    }
  }

  return (
    <Modal onClose={onClose} label="Create issue" width={640}>
      <form
        className="modal-body create-form"
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <h2>Create issue</h2>
        <div className="form-grid">
          <label>Project</label>
          <span className="field-static">
            <span className="project-avatar sm">{d.project?.key.slice(0, 2)}</span>
            {d.project?.name}
          </span>

          <label>Issue type</label>
          <TypePicker value={type} onChange={setType} />

          <label>Status</label>
          <select className="field-input" value={statusId} onChange={(e) => setStatusId(e.target.value)}>
            {d.statuses.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </select>

          <label htmlFor="ci-title">
            Summary <span className="req">*</span>
          </label>
          <input id="ci-title" className="field-input" autoFocus required maxLength={255} value={title} onChange={(e) => setTitle(e.target.value)} />

          <label htmlFor="ci-desc">Description</label>
          <textarea id="ci-desc" className="field-input" rows={4} value={description} onChange={(e) => setDescription(e.target.value)} />

          <label>Assignee</label>
          <span className="row">
            <AssigneePicker value={assigneeId} onChange={setAssigneeId} />
            {user && assigneeId !== user.id && (
              <button type="button" className="link-btn small" onClick={() => setAssigneeId(user.id)}>
                Assign to me
              </button>
            )}
          </span>

          <label>Priority</label>
          <PriorityPicker value={priority} onChange={setPriority} />

          {type !== 'epic' && (
            <>
              <label htmlFor="ci-est">Estimate</label>
              <span className="estimate-field">
                <input id="ci-est" className="field-input" type="number" min={0} step={0.5} value={estimate} onChange={(e) => setEstimate(e.target.value)} />
                <span className="muted">days</span>
              </span>
            </>
          )}

          <label htmlFor="ci-due">Due date</label>
          <input id="ci-due" className="field-input" type="date" value={dueDate} onChange={(e) => setDueDate(e.target.value)} />

          <label>Labels</label>
          <LabelsEditor value={labels} onChange={setLabels} />

          {type !== 'epic' && (
            <>
              <label>Epic</label>
              <EpicPicker value={epicId} onChange={setEpicId} />

              <label>Blocked by</label>
              <div className="blocker-pick">
                {blockers.map((b) => {
                  const i = d.issueMap.get(b)!
                  return (
                    <span key={b} className="label-chip removable">
                      <TypeIcon type={i.type} size={12} /> {d.keyOf(i)}
                      <button type="button" onClick={() => setBlockers(blockers.filter((x) => x !== b))} aria-label="Remove">
                        <Icon name="x" size={12} />
                      </button>
                    </span>
                  )
                })}
                <IssuePicker
                  label="Add blocker"
                  exclude={new Set(blockers)}
                  disabled={(i) => (d.category(i) === 'done' ? 'already done' : null)}
                  onPick={(id) => !wouldCreateCycle(d.nodes, '__new', id) && setBlockers([...blockers, id])}
                />
              </div>
            </>
          )}
        </div>
        <div className="modal-actions">
          <label className="toggle-inline">
            <input type="checkbox" checked={another} onChange={(e) => setAnother(e.target.checked)} /> Create another
          </label>
          <span className="spacer" />
          <Button type="button" variant="subtle" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy || !title.trim()}>
            Create
          </Button>
        </div>
      </form>
    </Modal>
  )
}
