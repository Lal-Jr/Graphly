import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, errorMessage } from '../api'
import { CommandPalette } from '../components/CommandPalette'
import { TopNav } from '../components/TopNav'
import { formatDate } from '../lib/forecast'
import { useSession } from '../store/session'
import type { MyWorkItem, Project, ProjectSummary } from '../types'
import { Icon, PriorityIcon, TypeIcon } from '../ui/icons'
import { Button, EmptyState, Lozenge, Modal, Spinner } from '../ui/primitives'
import { useToast } from '../ui/toast'

export function Home() {
  const { user, workspace } = useSession()
  const [projects, setProjects] = useState<ProjectSummary[] | null>(null)
  const [work, setWork] = useState<MyWorkItem[] | null>(null)
  const [creating, setCreating] = useState(false)
  const nav = useNavigate()

  const load = useCallback(() => {
    if (!workspace) return
    setProjects(null)
    setWork(null)
    api.get<ProjectSummary[]>(`/workspaces/${workspace.id}/projects`).then(setProjects, () => setProjects([]))
    api.get<MyWorkItem[]>(`/workspaces/${workspace.id}/my-work`).then(setWork, () => setWork([]))
  }, [workspace])
  useEffect(load, [load])

  const projectOf = (id: string) => projects?.find((p) => p.id === id)
  const open = (i: MyWorkItem) => nav(`/p/${i.projectId}/board?issue=${i.projectKey}-${i.number}`)
  const groups = work && {
    // Unblocked work, with whatever teammates are waiting on first.
    next: work.filter((i) => i.category === 'todo' && i.openBlockers === 0).sort((a, b) => b.waiting - a.waiting),
    doing: work.filter((i) => i.category === 'in_progress'),
    waiting: work.filter((i) => i.category === 'todo' && i.openBlockers > 0),
    blockingOthers: work.filter((i) => i.waiting > 0 && i.openBlockers === 0).sort((a, b) => b.waiting - a.waiting),
  }
  const hour = new Date().getHours()

  return (
    <div className="app">
      <TopNav onCreate={() => setCreating(true)} createLabel="Create project" />
      <main className="home">
        <div className="home-inner">
          <h1>
            Good {hour < 12 ? 'morning' : hour < 18 ? 'afternoon' : 'evening'}, {user?.name.split(' ')[0]}
          </h1>

          <section>
            <div className="section-head">
              <h2>Projects</h2>
            </div>
            {projects === null ? (
              <Spinner />
            ) : (
              <div className="project-grid">
                {projects.map((p) => {
                  const total = p.openIssues + p.doneIssues
                  return (
                    <Link key={p.id} to={`/p/${p.id}/board`} className="project-card">
                      <span className="project-avatar lg">{p.key.slice(0, 2)}</span>
                      <b>{p.name}</b>
                      <span className="muted small">
                        {p.key} · {p.openIssues} open
                      </span>
                      <span className="meter" title={`${p.doneIssues} of ${total} done`}>
                        <span style={{ width: `${total ? (p.doneIssues / total) * 100 : 0}%` }} />
                      </span>
                    </Link>
                  )
                })}
                <button className="project-card project-card-new" onClick={() => setCreating(true)}>
                  <Icon name="plus" size={24} />
                  <b>Create project</b>
                  <span className="muted small">Start empty or with a sample plan</span>
                </button>
              </div>
            )}
          </section>

          <section>
            <div className="section-head">
              <h2>Your work</h2>
              <span className="muted small">Open issues assigned to you across {workspace?.name}</span>
            </div>
            {groups === null ? (
              <Spinner />
            ) : work!.length === 0 ? (
              <EmptyState icon={<Icon name="check-circle" size={32} />} title="Nothing assigned to you">
                <p className="muted">Issues assigned to you will show up here, sorted by what you can start now.</p>
              </EmptyState>
            ) : (
              <>
              {groups.blockingOthers.length > 0 && (
                <div className="waiting-banner">
                  <div className="waiting-head">
                    <Icon name="users" size={18} />
                    <b>
                      {groups.blockingOthers.reduce((s, i) => s + i.waiting, 0)} issues are waiting on you
                    </b>
                    <span className="muted small">Finishing these unblocks your teammates first.</span>
                  </div>
                  {groups.blockingOthers.map((i) => (
                    <button key={i.id} className="work-item" onClick={() => open(i)}>
                      <TypeIcon type={i.type} />
                      <span className="work-main">
                        <span className="truncate">{i.title}</span>
                        <span className="muted small">
                          {i.projectKey}-{i.number} · {projectOf(i.projectId)?.name}
                        </span>
                      </span>
                      <span className="signal signal-warning">
                        <Icon name="link" size={12} /> {i.waiting} waiting
                      </span>
                      <Lozenge category={i.category}>{i.statusName}</Lozenge>
                    </button>
                  ))}
                </div>
              )}
              <div className="work-columns">
                <WorkGroup title="Up next" note="Unblocked and ready to start" items={groups.next} onOpen={open} projectOf={projectOf} />
                <WorkGroup title="In progress" items={groups.doing} onOpen={open} projectOf={projectOf} />
                <WorkGroup title="Waiting on others" note="Blocked by open issues" items={groups.waiting} onOpen={open} projectOf={projectOf} />
              </div>
              </>
            )}
          </section>
        </div>
      </main>
      {creating && workspace && (
        <CreateProjectDialog
          workspaceId={workspace.id}
          onClose={() => setCreating(false)}
          onCreated={(p) => {
            setCreating(false)
            nav(`/p/${p.id}/board`)
          }}
        />
      )}
      <CommandPalette />
    </div>
  )
}

function WorkGroup({
  title,
  note,
  items,
  onOpen,
  projectOf,
}: {
  title: string
  note?: string
  items: MyWorkItem[]
  onOpen: (i: MyWorkItem) => void
  projectOf: (id: string) => Project | undefined
}) {
  return (
    <div className="work-group">
      <h3>
        {title} <span className="count-badge">{items.length}</span>
      </h3>
      {note && <p className="muted small">{note}</p>}
      {items.map((i) => (
        <button key={i.id} className="work-item" onClick={() => onOpen(i)}>
          <TypeIcon type={i.type} />
          <span className="work-main">
            <span className="truncate">{i.title}</span>
            <span className="muted small">
              {i.projectKey}-{i.number} · {projectOf(i.projectId)?.name}
              {i.dueDate && ` · due ${formatDate(i.dueDate)}`}
            </span>
          </span>
          {i.openBlockers > 0 && (
            <span className="signal signal-danger">
              <Icon name="block" size={12} /> {i.openBlockers}
            </span>
          )}
          <PriorityIcon priority={i.priority} />
          <Lozenge category={i.category}>{i.statusName}</Lozenge>
        </button>
      ))}
      {items.length === 0 && <p className="muted small empty-note">Nothing here.</p>}
    </div>
  )
}

function CreateProjectDialog({ workspaceId, onClose, onCreated }: { workspaceId: string; onClose: () => void; onCreated: (p: Project) => void }) {
  const toast = useToast()
  const [name, setName] = useState('')
  const [key, setKey] = useState('')
  const [keyTouched, setKeyTouched] = useState(false)
  const [sample, setSample] = useState(true)
  const [busy, setBusy] = useState(false)
  const suggested = name
    .toUpperCase()
    .replace(/[^A-Z0-9 ]/g, '')
    .split(/\s+/)
    .filter(Boolean)
    .map((w, _, all) => (all.length > 1 ? w[0] : w.slice(0, 4)))
    .join('')
    .slice(0, 10)
  const effectiveKey = keyTouched ? key : suggested

  return (
    <Modal onClose={onClose} label="Create project" width={520}>
      <form
        className="modal-body"
        onSubmit={async (e) => {
          e.preventDefault()
          setBusy(true)
          try {
            onCreated(await api.post<Project>(`/workspaces/${workspaceId}/projects`, { name, key: effectiveKey, sample }))
          } catch (err) {
            toast(errorMessage(err), 'error')
            setBusy(false)
          }
        }}
      >
        <h2>Create project</h2>
        <label className="stack-field">
          Name
          <input className="field-input" autoFocus required maxLength={80} value={name} onChange={(e) => setName(e.target.value)} placeholder="Mobile app launch" />
        </label>
        <label className="stack-field">
          Key
          <input
            className="field-input mono"
            required
            value={effectiveKey}
            onChange={(e) => {
              setKeyTouched(true)
              setKey(e.target.value.toUpperCase().replace(/[^A-Z0-9]/g, '').slice(0, 10))
            }}
            placeholder="MAL"
          />
          <span className="muted small">Prefixes every issue, like {effectiveKey || 'KEY'}-42. 2–10 letters or digits.</span>
        </label>
        <label className="check-card">
          <input type="checkbox" checked={sample} onChange={(e) => setSample(e.target.checked)} />
          <span>
            <b>Include a sample plan</b>
            <span className="muted small">An 18-issue launch with epics, dependencies and a due date that's at risk — handy for exploring forecasting.</span>
          </span>
        </label>
        <div className="modal-actions">
          <Button type="button" variant="subtle" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy || !name.trim() || effectiveKey.length < 2}>
            Create project
          </Button>
        </div>
      </form>
    </Modal>
  )
}
