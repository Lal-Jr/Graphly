import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, errorMessage } from '../api'
import { useSession } from '../store/session'
import { useProject } from '../store/useProject'
import type { Category, Member, Status } from '../types'
import { Icon } from '../ui/icons'
import { Avatar, Button, ConfirmDialog, Lozenge } from '../ui/primitives'
import { useToast } from '../ui/toast'

const CATEGORY_LABEL: Record<Category, string> = { todo: 'To do', in_progress: 'In progress', done: 'Done' }

export function SettingsView() {
  const d = useProject()
  const { workspace, user, refresh } = useSession()
  const toast = useToast()
  const nav = useNavigate()
  const isAdmin = workspace?.role === 'admin'
  const [name, setName] = useState(d.project?.name ?? '')
  const [description, setDescription] = useState(d.project?.description ?? '')
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [invite, setInvite] = useState<string | null>(null)

  useEffect(() => {
    setName(d.project?.name ?? '')
    setDescription(d.project?.description ?? '')
  }, [d.project?.name, d.project?.description])

  const wsId = d.project?.workspaceId
  const reloadMembers = async () => {
    if (wsId) d.store.refreshMembers(await api.get<Member[]>(`/workspaces/${wsId}/members`))
  }

  return (
    <div className="view view-settings">
      <div className="settings-scroll">
        <section className="settings-card">
          <h2>Details</h2>
          <form
            className="settings-form"
            onSubmit={(e) => {
              e.preventDefault()
              void d.store.updateProject({ name, description }).then(() => toast('Project saved', 'success'))
            }}
          >
            <label className="stack-field">
              Name
              <input className="field-input" value={name} onChange={(e) => setName(e.target.value)} required maxLength={80} />
            </label>
            <label className="stack-field">
              Key
              <input className="field-input" value={d.project?.key ?? ''} disabled />
              <span className="muted small">Issue keys like {d.project?.key}-1 are permanent, so the key can't change.</span>
            </label>
            <label className="stack-field">
              Description
              <textarea className="field-input" rows={3} value={description} onChange={(e) => setDescription(e.target.value)} />
            </label>
            <div>
              <Button variant="primary" type="submit" disabled={name === d.project?.name && description === d.project?.description}>
                Save
              </Button>
            </div>
          </form>
        </section>

        <section className="settings-card">
          <h2>Workflow</h2>
          <p className="muted">
            Columns on the board, in order. Each status belongs to a category — Graphly uses categories to decide what's blocked, in flight, or done.
          </p>
          <WorkflowEditor />
        </section>

        <section className="settings-card">
          <h2>People</h2>
          <p className="muted">Everyone in {workspace?.name} can see and edit this project.</p>
          <ul className="member-list">
            {d.members.map((m) => (
              <li key={m.id}>
                <Avatar user={m} size={32} />
                <div className="member-name">
                  <b>
                    {m.name}
                    {m.id === user?.id && <span className="muted"> (you)</span>}
                  </b>
                  <span className="muted small">{m.email}</span>
                </div>
                {isAdmin && m.id !== user?.id ? (
                  <>
                    <select
                      className="field-input sm"
                      value={m.role}
                      onChange={(e) =>
                        api
                          .patch(`/workspaces/${wsId}/members/${m.id}`, { role: e.target.value })
                          .then(reloadMembers, (err) => toast(errorMessage(err), 'error'))
                      }
                    >
                      <option value="admin">Admin</option>
                      <option value="member">Member</option>
                    </select>
                    <Button
                      size="sm"
                      variant="subtle"
                      onClick={() =>
                        api.del(`/workspaces/${wsId}/members/${m.id}`).then(() => {
                          toast(`Removed ${m.name}; their open issues are now unassigned`, 'success')
                          void reloadMembers()
                        }, (err) => toast(errorMessage(err), 'error'))
                      }
                    >
                      Remove
                    </Button>
                  </>
                ) : (
                  <Lozenge category={m.role === 'admin' ? 'discovery' : 'todo'}>{m.role}</Lozenge>
                )}
              </li>
            ))}
          </ul>
          {isAdmin ? (
            <div className="invite-box">
              {invite ? (
                <>
                  <input className="field-input" readOnly value={invite} onFocus={(e) => e.target.select()} />
                  <Button icon={<Icon name="copy" size={14} />} onClick={() => navigator.clipboard.writeText(invite).then(() => toast('Invite link copied', 'success'))}>
                    Copy
                  </Button>
                  <span className="muted small">Anyone with this link can join for 7 days.</span>
                </>
              ) : (
                <Button
                  icon={<Icon name="user-plus" size={16} />}
                  onClick={() =>
                    api.post<{ token: string }>(`/workspaces/${wsId}/invites`).then(
                      (r) => setInvite(`${location.origin}/invite/${r.token}`),
                      (err) => toast(errorMessage(err), 'error'),
                    )
                  }
                >
                  Create invite link
                </Button>
              )}
            </div>
          ) : (
            <p className="muted small">Ask a workspace admin to invite more people.</p>
          )}
        </section>

        <GitHubCard isAdmin={isAdmin} />

        {isAdmin && (
          <section className="settings-card danger-zone">
            <h2>Danger zone</h2>
            <p className="muted">Deleting a project removes all of its issues, comments and history for everyone.</p>
            <Button variant="danger" icon={<Icon name="trash" size={16} />} onClick={() => setConfirmDelete(true)}>
              Delete project
            </Button>
          </section>
        )}
      </div>
      {confirmDelete && (
        <ConfirmDialog
          title={`Delete ${d.project?.name}?`}
          body={`All ${d.issues.length} issues will be permanently deleted.`}
          confirm="Delete project"
          onCancel={() => setConfirmDelete(false)}
          onConfirm={() =>
            api.del(`/projects/${d.project!.id}`).then(
              () => {
                void refresh()
                nav('/')
              },
              (err) => toast(errorMessage(err), 'error'),
            )
          }
        />
      )}
    </div>
  )
}

function WorkflowEditor() {
  const d = useProject()
  const [adding, setAdding] = useState<{ name: string; category: Category } | null>(null)
  const [removing, setRemoving] = useState<Status | null>(null)
  const [moveTo, setMoveTo] = useState('')
  const count = (id: string) => d.issues.filter((i) => i.statusId === id).length

  return (
    <>
      <ul className="workflow">
        {d.statuses.map((s, k) => (
          <li key={s.id}>
            <span className="wf-order">
              <button className="icon-btn" disabled={k === 0} onClick={() => d.store.statusOp(() => api.patch(`/statuses/${s.id}`, { position: k - 1 }))} aria-label="Move up">
                <Icon name="chevron-down" size={14} className="flip" />
              </button>
              <button
                className="icon-btn"
                disabled={k === d.statuses.length - 1}
                onClick={() => d.store.statusOp(() => api.patch(`/statuses/${s.id}`, { position: k + 1 }))}
                aria-label="Move down"
              >
                <Icon name="chevron-down" size={14} />
              </button>
            </span>
            <input
              className="field-input"
              defaultValue={s.name}
              key={s.name}
              maxLength={40}
              onBlur={(e) => e.target.value.trim() && e.target.value !== s.name && d.store.statusOp(() => api.patch(`/statuses/${s.id}`, { name: e.target.value }))}
              onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()}
            />
            <select className="field-input sm" value={s.category} onChange={(e) => d.store.statusOp(() => api.patch(`/statuses/${s.id}`, { category: e.target.value }))}>
              {(Object.keys(CATEGORY_LABEL) as Category[]).map((c) => (
                <option key={c} value={c}>
                  {CATEGORY_LABEL[c]}
                </option>
              ))}
            </select>
            <span className="muted small wf-count">{count(s.id)} issues</span>
            <button
              className="icon-btn"
              disabled={d.statuses.length <= 1}
              onClick={() => {
                if (count(s.id) === 0) void d.store.statusOp(() => api.del(`/statuses/${s.id}`))
                else {
                  setMoveTo(d.statuses.find((x) => x.id !== s.id)!.id)
                  setRemoving(s)
                }
              }}
              aria-label={`Delete ${s.name}`}
            >
              <Icon name="trash" size={14} />
            </button>
          </li>
        ))}
      </ul>
      {adding ? (
        <form
          className="wf-add"
          onSubmit={async (e) => {
            e.preventDefault()
            if (await d.store.statusOp(() => api.post(`/projects/${d.project!.id}/statuses`, adding))) setAdding(null)
          }}
        >
          <input className="field-input" autoFocus placeholder="Status name" value={adding.name} onChange={(e) => setAdding({ ...adding, name: e.target.value })} maxLength={40} />
          <select className="field-input sm" value={adding.category} onChange={(e) => setAdding({ ...adding, category: e.target.value as Category })}>
            {(Object.keys(CATEGORY_LABEL) as Category[]).map((c) => (
              <option key={c} value={c}>
                {CATEGORY_LABEL[c]}
              </option>
            ))}
          </select>
          <Button type="submit" variant="primary" size="sm" disabled={!adding.name.trim()}>
            Add
          </Button>
          <Button type="button" variant="subtle" size="sm" onClick={() => setAdding(null)}>
            Cancel
          </Button>
        </form>
      ) : (
        <Button size="sm" icon={<Icon name="plus" size={14} />} onClick={() => setAdding({ name: '', category: 'in_progress' })}>
          Add status
        </Button>
      )}
      {removing && (
        <ConfirmDialog
          title={`Delete “${removing.name}”?`}
          body={
            <>
              Move its {count(removing.id)} issues to{' '}
              <select className="field-input sm inline" value={moveTo} onChange={(e) => setMoveTo(e.target.value)}>
                {d.statuses
                  .filter((x) => x.id !== removing.id)
                  .map((x) => (
                    <option key={x.id} value={x.id}>
                      {x.name}
                    </option>
                  ))}
              </select>
            </>
          }
          confirm="Delete status"
          onCancel={() => setRemoving(null)}
          onConfirm={() => {
            const s = removing
            setRemoving(null)
            void d.store.statusOp(() => api.del(`/statuses/${s.id}`, { moveTo }))
          }}
        />
      )}
    </>
  )
}

/** Connect a repository so pull requests move issues on their own. */
function GitHubCard({ isAdmin }: { isAdmin: boolean }) {
  const d = useProject()
  const toast = useToast()
  const [state, setState] = useState<{ enabled: boolean; path: string; secret?: string } | null>(null)
  const id = d.project?.id
  useEffect(() => {
    if (id) api.get<{ enabled: boolean; path: string }>(`/projects/${id}/github`).then(setState, () => {})
  }, [id])
  if (!state) return null
  const url = `${location.origin}${state.path}`
  const copy = (v: string, what: string) => navigator.clipboard.writeText(v).then(() => toast(`${what} copied`, 'success'))
  return (
    <section className="settings-card">
      <h2>
        <Icon name="link" size={16} /> GitHub
      </h2>
      <p className="muted">
        Mention an issue key such as <code>{d.project?.key}-12</code> in a pull request's title, branch or description. Opening the PR
        moves the issue to review, merging moves it to done and unblocks whatever was waiting on it, and the PR appears on the
        issue. Nobody has to update tickets by hand.
      </p>
      {state.enabled ? (
        <>
          <div className="gh-grid">
            <span className="muted small">Payload URL</span>
            <span className="row">
              <input className="field-input mono" readOnly value={url} onFocus={(e) => e.target.select()} />
              <Button size="sm" icon={<Icon name="copy" size={14} />} onClick={() => copy(url, 'Payload URL')} />
            </span>
            <span className="muted small">Secret</span>
            {state.secret ? (
              <span className="row">
                <input className="field-input mono" readOnly value={state.secret} onFocus={(e) => e.target.select()} />
                <Button size="sm" icon={<Icon name="copy" size={14} />} onClick={() => copy(state.secret!, 'Secret')} />
              </span>
            ) : (
              <span className="muted small">Hidden. Regenerate it if you need to set it again.</span>
            )}
            <span className="muted small">Settings</span>
            <span className="small">
              Content type <code>application/json</code>, and send only <b>Pull requests</b> events.
            </span>
          </div>
          {state.secret && <p className="small text-warning">Copy the secret now. It won't be shown again.</p>}
          {isAdmin && (
            <div className="row">
              <Button size="sm" onClick={() => api.post<typeof state>(`/projects/${id}/github`).then(setState)}>
                Regenerate secret
              </Button>
              <Button size="sm" variant="subtle" onClick={() => api.del<typeof state>(`/projects/${id}/github`).then(() => setState({ ...state, enabled: false, secret: undefined }))}>
                Disconnect
              </Button>
            </div>
          )}
        </>
      ) : isAdmin ? (
        <div>
          <Button variant="primary" icon={<Icon name="link" size={16} />} onClick={() => api.post<typeof state>(`/projects/${id}/github`).then(setState, (e) => toast(errorMessage(e), 'error'))}>
            Connect GitHub
          </Button>
        </div>
      ) : (
        <p className="muted small">Ask a workspace admin to connect a repository.</p>
      )}
    </section>
  )
}
