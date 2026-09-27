import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, errorMessage } from '../api'
import { addWorkdays, formatDate, nextWorkday, simulateSlip } from '../lib/forecast'
import { wouldCreateCycle } from '../lib/graph'
import { timeAgo } from '../lib/time'
import { useIssueParam } from '../store/nav'
import { useSession } from '../store/session'
import { useProject, useViewers } from '../store/useProject'
import { PRIORITY_LABEL, TYPE_LABEL, type Activity, type Comment, type Issue, type PullRequest } from '../types'
import { Icon, TypeIcon } from '../ui/icons'
import { Avatar, AvatarStack, Button, ConfirmDialog, InlineEdit, Lozenge, MenuList, Modal, Popover } from '../ui/primitives'
import { useToast } from '../ui/toast'
import { AssigneePicker, EpicPicker, IssuePicker, LabelsEditor, PriorityPicker, StatusButton, TypePicker } from './fields'

export function IssueModal() {
  const d = useProject()
  const [key, open] = useIssueParam()
  const issue = key ? d.byKey(key) : undefined
  const close = useCallback(() => open(null), [open])

  useEffect(() => {
    if (!issue) return
    d.store.setPresence({ viewing: issue.id })
    return () => d.store.setPresence({ viewing: null, editing: null })
  }, [issue?.id, d.store]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!key || d.phase !== 'ready') return null
  if (!issue)
    return (
      <Modal onClose={close} label="Issue not found" width={420}>
        <div className="modal-body">
          <h2>{key} doesn't exist</h2>
          <p className="muted">It may have been deleted, or the link is wrong.</p>
          <div className="modal-actions">
            <Button variant="primary" onClick={close}>
              Close
            </Button>
          </div>
        </div>
      </Modal>
    )
  return (
    <Modal onClose={close} label={`${key} ${issue.title}`} width={1080} className="issue-modal">
      <IssueDetail issue={issue} onClose={close} />
    </Modal>
  )
}

function IssueDetail({ issue, onClose }: { issue: Issue; onClose: () => void }) {
  const d = useProject()
  const { user } = useSession()
  const toast = useToast()
  const [, openIssue] = useIssueParam()
  const viewers = useViewers(issue.id, user?.id)
  const key = d.keyOf(issue)
  const epic = issue.epicId ? d.issueMap.get(issue.epicId) : null
  const update = (fields: Partial<Issue>) => d.store.updateIssue(issue.id, fields)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const copyLink = () => {
    const url = `${location.origin}${location.pathname}?issue=${key}`
    navigator.clipboard.writeText(url).then(() => toast('Link copied', 'success'))
  }

  return (
    <div className="issue-detail">
      <header className="issue-head">
        <nav className="crumbs">
          {epic && (
            <>
              <button className="crumb" onClick={() => openIssue(d.keyOf(epic))}>
                <TypeIcon type="epic" size={14} />
                {d.keyOf(epic)}
              </button>
              <span className="muted">/</span>
            </>
          )}
          <TypePicker value={issue.type} iconOnly onChange={(type) => update({ type })} allowEpic={!issue.epicId} />
          <span className="crumb current">{key}</span>
        </nav>
        <span className="spacer" />
        {viewers.length > 0 && (
          <span className="viewing-now" title={viewers.map((v) => v.name).join(', ')}>
            <AvatarStack users={viewers.map((v) => ({ ...v, key: v.userId }))} size={24} />
            <span className="muted small">viewing</span>
          </span>
        )}
        <button className="icon-btn" onClick={copyLink} title="Copy link">
          <Icon name="link" size={18} />
        </button>
        <Popover
          align="end"
          width={200}
          trigger={({ toggle, ref }) => (
            <button ref={ref} className="icon-btn" onClick={toggle} title="More actions">
              <Icon name="more" size={18} />
            </button>
          )}
        >
          {(close) => (
            <MenuList
              items={[{ value: 'delete', label: 'Delete issue', icon: <Icon name="trash" size={14} />, danger: true }]}
              onSelect={() => {
                close()
                setConfirmDelete(true)
              }}
            />
          )}
        </Popover>
        <button className="icon-btn" onClick={onClose} title="Close (Esc)">
          <Icon name="x" size={18} />
        </button>
      </header>

      {confirmDelete && (
        <ConfirmDialog
          title={`Delete ${key}?`}
          body={
            <>
              This permanently deletes the issue, its comments and its dependency links for everyone.
              {d.blocksOf(issue.id).length > 0 && ` ${d.blocksOf(issue.id).length} issues that wait on it will be unblocked.`}
            </>
          }
          confirm="Delete"
          onCancel={() => setConfirmDelete(false)}
          onConfirm={() => {
            onClose()
            void d.store.deleteIssue(issue.id)
          }}
        />
      )}

      <div className="issue-cols">
        <main className="issue-main">
          <TitleEditor issue={issue} />
          <DescriptionEditor issue={issue} />
          {issue.type === 'epic' ? <ChildIssues epic={issue} /> : <Dependencies issue={issue} />}
          <Development issue={issue} />
          <ActivityPanel issue={issue} />
        </main>

        <aside className="issue-side">
          <div className="side-status">
            <StatusButton issue={issue} onChange={(statusId) => update({ statusId })} />
            {d.analysis.blockedBy.has(issue.id) && (
              <span className="signal signal-danger">
                <Icon name="block" size={12} /> Blocked
              </span>
            )}
          </div>

          <section className="details">
            <h4>Details</h4>
            <dl>
              <dt>Assignee</dt>
              <dd>
                <AssigneePicker value={issue.assigneeId} onChange={(assigneeId) => update({ assigneeId })} />
                {user && issue.assigneeId !== user.id && d.memberMap.has(user.id) && (
                  <button className="link-btn small" onClick={() => update({ assigneeId: user.id })}>
                    Assign to me
                  </button>
                )}
              </dd>
              <dt>Reporter</dt>
              <dd>
                <span className="field-static">
                  <Avatar user={issue.reporterId ? d.memberMap.get(issue.reporterId) : null} size={24} />
                  {d.memberMap.get(issue.reporterId ?? '')?.name ?? <span className="muted">Former member</span>}
                </span>
              </dd>
              <dt>Priority</dt>
              <dd>
                <PriorityPicker value={issue.priority} onChange={(priority) => update({ priority })} />
              </dd>
              {issue.type !== 'epic' && (
                <>
                  <dt>Estimate</dt>
                  <dd>
                    <EstimateInput value={issue.estimate} onChange={(estimate) => update({ estimate })} />
                  </dd>
                </>
              )}
              <dt>Due date</dt>
              <dd>
                <input
                  type="date"
                  className="field-input"
                  value={issue.dueDate ?? ''}
                  onChange={(e) => update({ dueDate: e.target.value || null })}
                />
              </dd>
              <dt>Labels</dt>
              <dd>
                <LabelsEditor value={issue.labels} onChange={(labels) => update({ labels })} />
              </dd>
              {issue.type !== 'epic' && (
                <>
                  <dt>Epic</dt>
                  <dd>
                    <EpicPicker value={issue.epicId} onChange={(epicId) => update({ epicId })} />
                  </dd>
                </>
              )}
            </dl>
          </section>

          {issue.type !== 'epic' && <ForecastPanel issue={issue} />}

          <p className="muted small timestamps">
            Created {timeAgo(issue.createdAt)}
            <br />
            Updated {timeAgo(issue.updatedAt)}
          </p>
        </aside>
      </div>
    </div>
  )
}

function EstimateInput({ value, onChange }: { value: number; onChange: (v: number) => void }) {
  const [draft, setDraft] = useState(String(value))
  useEffect(() => setDraft(String(value)), [value])
  const commit = () => {
    const n = Math.max(0, Math.min(1000, Number(draft)))
    if (Number.isFinite(n) && n !== value) onChange(n)
    else setDraft(String(value))
  }
  return (
    <span className="estimate-field">
      <input
        type="number"
        min={0}
        step={0.5}
        className="field-input"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()}
      />
      <span className="muted">days</span>
    </span>
  )
}

function TitleEditor({ issue }: { issue: Issue }) {
  const { store } = useProject()
  const toast = useToast()
  return (
    <InlineEdit
      className="issue-title"
      value={issue.title}
      onEditingChange={(on) => store.setPresence({ editing: on ? 'title' : null })}
      onCommit={async (title) => {
        const res = await store.updateIssue(issue.id, { title }, { title: issue.title })
        if (res === 'conflict') toast('Someone renamed this issue while you were typing — showing their title', 'warning')
      }}
    />
  )
}

function DescriptionEditor({ issue }: { issue: Issue }) {
  const d = useProject()
  const { user } = useSession()
  const [draft, setDraft] = useState<string | null>(null)
  const [base, setBase] = useState('')
  const [conflict, setConflict] = useState(false)
  const others = d.presence.filter((p) => p.viewing === issue.id && p.editing === 'description' && p.userId !== user?.id)

  const start = () => {
    setBase(issue.description)
    setDraft(issue.description)
    setConflict(false)
    d.store.setPresence({ editing: 'description' })
  }
  const stop = () => {
    setDraft(null)
    setConflict(false)
    d.store.setPresence({ editing: null })
  }
  const save = async (force = false) => {
    if (draft === null) return
    const res = await d.store.updateIssue(issue.id, { description: draft }, { description: force ? issue.description : base })
    if (res === 'conflict') setConflict(true)
    else if (res === 'ok') stop()
  }

  return (
    <section className="description">
      <h4>Description</h4>
      {others.length > 0 && (
        <div className="editing-banner">
          <AvatarStack users={others.map((o) => ({ ...o, key: o.userId }))} size={18} />
          {others.map((o) => o.name).join(', ')} {others.length === 1 ? 'is' : 'are'} editing the description
        </div>
      )}
      {draft === null ? (
        <div className="description-view" role="button" tabIndex={0} onClick={start} onKeyDown={(e) => e.key === 'Enter' && start()}>
          {issue.description || <span className="placeholder">Add a description…</span>}
        </div>
      ) : (
        <>
          <textarea
            className="description-input"
            autoFocus
            rows={Math.min(16, Math.max(5, draft.split('\n').length + 1))}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) void save()
              if (e.key === 'Escape') {
                e.stopPropagation()
                stop()
              }
            }}
          />
          {conflict && (
            <div className="conflict">
              <b>
                <Icon name="warning" size={14} /> Someone else changed the description while you were editing.
              </b>
              <div className="conflict-theirs">{issue.description || <em className="muted">(now empty)</em>}</div>
              <div className="row">
                <Button size="sm" onClick={stop}>
                  Discard mine, keep theirs
                </Button>
                <Button size="sm" variant="warning" onClick={() => save(true)}>
                  Overwrite with mine
                </Button>
              </div>
            </div>
          )}
          {!conflict && (
            <div className="row">
              <Button size="sm" variant="primary" onClick={() => save()}>
                Save
              </Button>
              <Button size="sm" variant="subtle" onClick={stop}>
                Cancel
              </Button>
              <span className="muted small">⌘↵ to save</span>
            </div>
          )}
        </>
      )}
    </section>
  )
}

function Dependencies({ issue }: { issue: Issue }) {
  const d = useProject()
  const [, openIssue] = useIssueParam()
  const blockers = d.blockersOf(issue.id)
  const blocks = d.blocksOf(issue.id)
  const linked = new Set([issue.id, ...blockers.map((b) => b.id), ...blocks.map((b) => b.id)])

  const Row = ({ other, onRemove }: { other: Issue; onRemove: () => void }) => {
    const st = d.statusMap.get(other.statusId)
    return (
      <li className="dep-row">
        <button className="dep-main" onClick={() => openIssue(d.keyOf(other))}>
          <TypeIcon type={other.type} size={14} />
          <span className={`issue-key${st?.category === 'done' ? ' done' : ''}`}>{d.keyOf(other)}</span>
          <span className="truncate">{other.title}</span>
        </button>
        <Avatar user={other.assigneeId ? d.memberMap.get(other.assigneeId) : null} size={20} />
        {st && <Lozenge category={st.category}>{st.name}</Lozenge>}
        <button className="icon-btn" onClick={onRemove} title="Remove link">
          <Icon name="x" size={14} />
        </button>
      </li>
    )
  }

  return (
    <section className="dependencies">
      <h4>Dependencies</h4>
      <div className="dep-group">
        <div className="dep-label">
          <span>is blocked by</span>
          <IssuePicker
            label="Add blocker"
            exclude={linked}
            disabled={(other) => (wouldCreateCycle(d.nodes, issue.id, other.id) ? 'would create a cycle' : null)}
            onPick={(id) => d.store.addBlocker(issue.id, id)}
          />
        </div>
        <ul>
          {blockers.map((b) => (
            <Row key={b.id} other={b} onRemove={() => d.store.removeBlocker(issue.id, b.id)} />
          ))}
          {blockers.length === 0 && <li className="muted small">Nothing — this can start any time.</li>}
        </ul>
      </div>
      <div className="dep-group">
        <div className="dep-label">
          <span>blocks</span>
          <IssuePicker
            label="Add blocked issue"
            exclude={linked}
            disabled={(other) => (wouldCreateCycle(d.nodes, other.id, issue.id) ? 'would create a cycle' : null)}
            onPick={(id) => d.store.addBlocker(id, issue.id)}
          />
        </div>
        <ul>
          {blocks.map((b) => (
            <Row key={b.id} other={b} onRemove={() => d.store.removeBlocker(b.id, issue.id)} />
          ))}
          {blocks.length === 0 && <li className="muted small">Nothing is waiting on this.</li>}
        </ul>
      </div>
    </section>
  )
}

/** Pull requests that mention this issue's key, kept current by the GitHub webhook. */
function Development({ issue }: { issue: Issue }) {
  const d = useProject()
  const [prs, setPrs] = useState<PullRequest[]>([])
  useEffect(() => {
    let live = true
    api.get<PullRequest[]>(`/issues/${issue.id}/prs`).then((p) => live && setPrs(p), () => {})
    const off = d.store.onFeed((e) => {
      if (e.type === 'pr.update' && e.issueId === issue.id)
        setPrs((cur) => [e.pr, ...cur.filter((p) => !(p.repo === e.pr.repo && p.number === e.pr.number))])
    })
    return () => {
      live = false
      off()
    }
  }, [issue.id, d.store])
  if (prs.length === 0) return null
  return (
    <section className="development">
      <h4>Development</h4>
      <ul>
        {prs.map((p) => (
          <li key={`${p.repo}#${p.number}`}>
            <a href={p.url} target="_blank" rel="noreferrer" className="pr-row">
              <span className={`pr-state pr-${p.state}`}>{p.state}</span>
              <span className="truncate">{p.title}</span>
              <span className="muted small">
                {p.repo}#{p.number} · {p.author}
              </span>
            </a>
          </li>
        ))}
      </ul>
    </section>
  )
}

function ChildIssues({ epic }: { epic: Issue }) {
  const d = useProject()
  const [, openIssue] = useIssueParam()
  const [title, setTitle] = useState('')
  const kids = d.childrenOf(epic.id)
  const done = kids.filter((k) => d.category(k) === 'done').length
  return (
    <section className="dependencies">
      <h4>
        Child issues{' '}
        <span className="muted small">
          {done} of {kids.length} done
        </span>
      </h4>
      <span className="meter meter-block">
        <span style={{ width: `${kids.length ? (done / kids.length) * 100 : 0}%` }} />
      </span>
      <ul>
        {kids.map((k) => {
          const st = d.statusMap.get(k.statusId)
          return (
            <li key={k.id} className="dep-row">
              <button className="dep-main" onClick={() => openIssue(d.keyOf(k))}>
                <TypeIcon type={k.type} size={14} />
                <span className="issue-key">{d.keyOf(k)}</span>
                <span className="truncate">{k.title}</span>
              </button>
              <Avatar user={k.assigneeId ? d.memberMap.get(k.assigneeId) : null} size={20} />
              {st && <Lozenge category={st.category}>{st.name}</Lozenge>}
            </li>
          )
        })}
      </ul>
      <form
        className="add-child"
        onSubmit={(e) => {
          e.preventDefault()
          if (!title.trim()) return
          void d.store.createIssue({ title: title.trim(), epicId: epic.id })
          setTitle('')
        }}
      >
        <input className="field-input" placeholder="+ Add a child issue" value={title} onChange={(e) => setTitle(e.target.value)} />
      </form>
    </section>
  )
}

function ForecastPanel({ issue }: { issue: Issue }) {
  const d = useProject()
  const sched = d.analysis.schedule.get(issue.id)
  const fc = d.forecasts.get(issue.id)
  const critical = d.analysis.criticalSet.has(issue.id)
  const impact = d.analysis.impact.get(issue.id) ?? 0
  const p85 = d.confidence.p85ById.get(issue.id)
  const onTime = d.confidence.onTime.get(issue.id)
  const sim = useMemo(() => (sched ? simulateSlip(d.nodes, d.dueDates, issue.id, 3) : null), [sched, d.nodes, d.dueDates, issue.id])

  if (d.category(issue) === 'done')
    return (
      <section className="forecast-panel">
        <h4>Forecast</h4>
        <p className="muted">
          <Icon name="check-circle" size={14} className="text-success" /> Done
          {impact === 0 && d.blocksOf(issue.id).length > 0 && ' — no longer blocking anyone'}
        </p>
      </section>
    )
  if (!sched || !fc)
    return (
      <section className="forecast-panel">
        <h4>Forecast</h4>
        <p className="muted">Can't schedule this issue — it's part of a dependency cycle.</p>
      </section>
    )
  return (
    <section className="forecast-panel">
      <h4>
        <Icon name="sparkles" size={14} /> Forecast
      </h4>
      <dl>
        <dt>Starts</dt>
        <dd>{formatDate(fc.start)}</dd>
        <dt>Finishes</dt>
        <dd className={fc.daysLate > 0 ? 'text-danger' : ''}>
          {formatDate(fc.finish)}
          {fc.daysLate > 0 && ` · ${fc.daysLate}d after due`}
        </dd>
        {p85 !== undefined && (
          <>
            <dt>85% likely by</dt>
            <dd>{formatDate(addWorkdays(nextWorkday(new Date()), Math.max(0, Math.ceil(p85) - 1)))}</dd>
          </>
        )}
        {onTime !== undefined && (
          <>
            <dt>Due-date odds</dt>
            <dd>
              <span className={`odds ${onTime >= 0.85 ? 'good' : onTime >= 0.5 ? 'fair' : 'poor'}`}>{Math.round(onTime * 100)}% chance</span>
              <span className="muted small">of finishing by {formatDate(issue.dueDate!)}</span>
            </dd>
          </>
        )}
        <dt>Slack</dt>
        <dd>{critical ? <b className="text-critical">None — on the critical path</b> : `${sched.slack} working days`}</dd>
        {impact > 0 && (
          <>
            <dt>Holding up</dt>
            <dd>
              {impact} open {impact === 1 ? 'issue' : 'issues'}
            </dd>
          </>
        )}
      </dl>
      {sim && (
        <p className="whatif-inline">
          If this slips 3 days:{' '}
          {sim.after > sim.before ? (
            <span className="text-danger">project finish +{Math.ceil(sim.after) - Math.ceil(sim.before)}d</span>
          ) : (
            <span className="text-success">no effect on the finish date</span>
          )}
          {sim.newlyLate.length > 0 && <span className="text-danger">, {sim.newlyLate.length} more due dates missed</span>}.
        </p>
      )}
    </section>
  )
}

function ActivityPanel({ issue }: { issue: Issue }) {
  const d = useProject()
  const { user } = useSession()
  const toast = useToast()
  const [tab, setTab] = useState<'comments' | 'history'>('comments')
  const [comments, setComments] = useState<Comment[] | null>(null)
  const [history, setHistory] = useState<Activity[] | null>(null)
  const [draft, setDraft] = useState('')
  const [posting, setPosting] = useState(false)

  useEffect(() => {
    let live = true
    setComments(null)
    setHistory(null)
    api.get<Comment[]>(`/issues/${issue.id}/comments`).then((c) => live && setComments(c), () => live && setComments([]))
    api.get<Activity[]>(`/issues/${issue.id}/activity`).then((a) => live && setHistory(a), () => live && setHistory([]))
    const off = d.store.onFeed((e) => {
      if (e.type === 'comment.add' && e.comment.issueId === issue.id)
        setComments((c) => (c && !c.some((x) => x.id === e.comment.id) ? [...c, e.comment] : c))
      if (e.type === 'comment.delete' && e.issueId === issue.id) setComments((c) => c?.filter((x) => x.id !== e.id) ?? c)
      if (e.type === 'activity.add' && e.activity.issueId === issue.id)
        setHistory((h) => (h && !h.some((x) => x.id === e.activity.id) ? [e.activity, ...h] : h))
    })
    return () => {
      live = false
      off()
    }
  }, [issue.id, d.store])

  const post = async () => {
    if (!draft.trim()) return
    setPosting(true)
    try {
      const c = await api.post<Comment>(`/issues/${issue.id}/comments`, { body: draft })
      setComments((cs) => (cs && !cs.some((x) => x.id === c.id) ? [...cs, c] : cs))
      setDraft('')
    } catch (e) {
      toast(errorMessage(e), 'error')
    } finally {
      setPosting(false)
    }
  }

  return (
    <section className="activity">
      <h4>Activity</h4>
      <div className="tabs-inline" role="tablist">
        <button role="tab" aria-selected={tab === 'comments'} className={tab === 'comments' ? 'on' : ''} onClick={() => setTab('comments')}>
          Comments {comments && comments.length > 0 && <span className="count-badge">{comments.length}</span>}
        </button>
        <button role="tab" aria-selected={tab === 'history'} className={tab === 'history' ? 'on' : ''} onClick={() => setTab('history')}>
          History
        </button>
      </div>

      {tab === 'comments' ? (
        <>
          <div className="composer">
            <Avatar user={user} size={32} />
            <div className="composer-body">
              <textarea
                placeholder="Add a comment…"
                rows={draft ? 3 : 1}
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && (e.metaKey || e.ctrlKey) && void post()}
              />
              {draft && (
                <div className="row">
                  <Button size="sm" variant="primary" disabled={posting} onClick={post}>
                    Save
                  </Button>
                  <Button size="sm" variant="subtle" onClick={() => setDraft('')}>
                    Cancel
                  </Button>
                </div>
              )}
            </div>
          </div>
          {comments === null && <p className="muted small">Loading…</p>}
          {[...(comments ?? [])].reverse().map((c) => {
            const author = c.authorId ? d.memberMap.get(c.authorId) : null
            return (
              <div key={c.id} className="comment">
                <Avatar user={author} size={32} />
                <div>
                  <div className="comment-meta">
                    <b>{author?.name ?? 'Former member'}</b>
                    <span className="muted small">{timeAgo(c.createdAt)}</span>
                  </div>
                  <div className="comment-body">{c.body}</div>
                  {c.authorId === user?.id && (
                    <button
                      className="link-btn small muted"
                      onClick={() => api.del(`/comments/${c.id}`).then(() => setComments((cs) => cs?.filter((x) => x.id !== c.id) ?? cs), (e) => toast(errorMessage(e), 'error'))}
                    >
                      Delete
                    </button>
                  )}
                </div>
              </div>
            )
          })}
        </>
      ) : (
        <ul className="history">
          {history === null && <li className="muted small">Loading…</li>}
          {history?.map((a) => (
            <li key={a.id}>
              <Avatar user={a.actorId ? d.memberMap.get(a.actorId) : null} size={24} />
              <span>
                <b>{a.actorId ? (d.memberMap.get(a.actorId)?.name ?? 'Former member') : 'GitHub'}</b> {describe(a, d)}{' '}
                <span className="muted small">{timeAgo(a.createdAt)}</span>
              </span>
            </li>
          ))}
          {history?.length === 0 && <li className="muted small">No history yet.</li>}
        </ul>
      )}
    </section>
  )
}

function describe(a: Activity, d: ReturnType<typeof useProject>): React.ReactNode {
  const issueKey = (id: unknown) => {
    const i = typeof id === 'string' ? d.issueMap.get(id) : undefined
    return i ? d.keyOf(i) : 'a deleted issue'
  }
  const data = a.data as Record<string, unknown>
  switch (a.kind) {
    case 'created':
      return 'created the issue'
    case 'unblocked':
      return (
        <>
          <span className="text-success">unblocked this</span> by completing {issueKey(data.by)}
        </>
      )
    case 'pr':
      return (
        <>
          {data.state === 'merged' ? 'merged' : data.state === 'closed' ? 'closed' : data.state === 'draft' ? 'opened a draft of' : 'opened'} pull request{' '}
          <a href={String(data.url)} target="_blank" rel="noreferrer">
            {String(data.repo)}#{String(data.number)}
          </a>
        </>
      )
    case 'blocker.add':
      return <>marked this as blocked by {issueKey(data.blocker)}</>
    case 'blocker.remove':
      return <>removed blocker {issueKey(data.blocker)}</>
    case 'updated': {
      const f = String(data.field)
      const from = data.from
      const to = data.to
      if (f === 'status' && data.via === 'github')
        return <>moved this to <b>{d.statusMap.get(String(to))?.name ?? '?'}</b> from PR #{String(data.pr)}</>
      if (f === 'status') return <>changed status from <b>{d.statusMap.get(String(from))?.name ?? '?'}</b> to <b>{d.statusMap.get(String(to))?.name ?? '?'}</b></>
      if (f === 'assignee') return to ? <>assigned this to <b>{d.memberMap.get(String(to))?.name ?? 'someone'}</b></> : 'unassigned this'
      if (f === 'priority') return <>changed priority to <b>{PRIORITY_LABEL[to as keyof typeof PRIORITY_LABEL] ?? String(to)}</b></>
      if (f === 'estimate') return <>changed the estimate from {String(from)}d to <b>{String(to)}d</b></>
      if (f === 'dueDate') return to ? <>set the due date to <b>{formatDate(String(to))}</b></> : 'removed the due date'
      if (f === 'type') return <>changed the type to <b>{TYPE_LABEL[to as keyof typeof TYPE_LABEL] ?? String(to)}</b></>
      if (f === 'title') return <>renamed this from “{String(from)}”</>
      return `updated the ${f === 'epic' ? 'epic' : f}`
    }
    default:
      return a.kind
  }
}
