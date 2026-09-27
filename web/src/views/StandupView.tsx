import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { api } from '../api'
import { addWorkdays, formatDate, nextWorkday, parseDate, toISODate } from '../lib/forecast'
import { useIssueParam } from '../store/nav'
import { useProject } from '../store/useProject'
import type { Activity, ForecastSnapshot, Issue } from '../types'
import { Icon, TypeIcon } from '../ui/icons'
import { Avatar, Button, EmptyState, Spinner } from '../ui/primitives'
import { useToast } from '../ui/toast'

type Window = 'workday' | 'week'

/** Start of the standup window: the previous working day at this time (Friday, on a Monday), or a week ago. */
function windowStart(w: Window, now = new Date()) {
  const d = new Date(now)
  if (w === 'week') d.setDate(d.getDate() - 7)
  else {
    do d.setDate(d.getDate() - 1)
    while (d.getDay() === 0 || d.getDay() === 6)
  }
  return d
}

interface Digest {
  shipped: Issue[]
  started: Issue[]
  unblocked: { issue: Issue; by?: Issue }[]
  newlyBlocked: { issue: Issue; blocker?: Issue }[]
  added: Issue[]
  reestimated: { issue: Issue; from: number; to: number }[]
  prs: { issue: Issue; state: string; number: number; url: string; repo: string }[]
  touched: number
}

/**
 * An async standup: what changed since the last one, what's newly stuck, and — the question
 * every status meeting circles around — whether the date moved, and why.
 */
export function StandupView() {
  const d = useProject()
  const toast = useToast()
  const [, openIssue] = useIssueParam()
  const [win, setWin] = useState<Window>('workday')
  const [acts, setActs] = useState<Activity[] | null>(null)
  const [snaps, setSnaps] = useState<ForecastSnapshot[]>([])
  const since = useMemo(() => windowStart(win), [win])
  const projectId = d.project?.id

  useEffect(() => {
    if (!projectId) return
    setActs(null)
    api.get<Activity[]>(`/projects/${projectId}/activity?since=${encodeURIComponent(since.toISOString())}`).then(setActs, () => setActs([]))
    api.get<ForecastSnapshot[]>(`/projects/${projectId}/snapshots`).then(setSnaps, () => setSnaps([]))
  }, [projectId, since])

  // Keep the digest live as new activity streams in.
  useEffect(
    () => d.store.onFeed((e) => e.type === 'activity.add' && setActs((a) => (a && !a.some((x) => x.id === e.activity.id) ? [e.activity, ...a] : a))),
    [d.store],
  )

  const digest = useMemo<Digest | null>(() => {
    if (!acts) return null
    const get = (id: unknown) => (typeof id === 'string' ? d.issueMap.get(id) : undefined)
    const uniq = <T,>(xs: T[], key: (x: T) => string) => [...new Map(xs.map((x) => [key(x), x])).values()]
    const out: Digest = { shipped: [], started: [], unblocked: [], newlyBlocked: [], added: [], reestimated: [], prs: [], touched: 0 }
    const touched = new Set<string>()
    for (const a of [...acts].reverse()) {
      const issue = get(a.issueId)
      if (!issue) continue
      touched.add(issue.id)
      const data = a.data
      if (a.kind === 'created') out.added.push(issue)
      if (a.kind === 'unblocked') out.unblocked.push({ issue, by: get(data.by) })
      if (a.kind === 'blocker.add') out.newlyBlocked.push({ issue, blocker: get(data.blocker) })
      if (a.kind === 'pr') out.prs.push({ issue, state: String(data.state), number: Number(data.number), url: String(data.url), repo: String(data.repo) })
      if (a.kind === 'updated' && data.field === 'status') {
        const to = d.statusMap.get(String(data.to))?.category
        const from = d.statusMap.get(String(data.from))?.category
        if (to === 'done' && from !== 'done') out.shipped.push(issue)
        if (to === 'in_progress' && from === 'todo') out.started.push(issue)
      }
      if (a.kind === 'updated' && data.field === 'estimate') {
        const prev = out.reestimated.find((r) => r.issue.id === issue.id)
        if (prev) prev.to = Number(data.to)
        else out.reestimated.push({ issue, from: Number(data.from), to: Number(data.to) })
      }
    }
    // Only report things that are still true: shipped work that's still done, blockers that still block.
    out.shipped = uniq(out.shipped.filter((i) => d.category(i) === 'done'), (i) => i.id)
    out.started = uniq(out.started.filter((i) => d.category(i) === 'in_progress'), (i) => i.id)
    out.unblocked = uniq(out.unblocked, (u) => u.issue.id)
    out.newlyBlocked = uniq(out.newlyBlocked.filter((b) => d.analysis.blockedBy.has(b.issue.id)), (b) => b.issue.id)
    out.added = uniq(out.added.filter((i) => i.type !== 'epic'), (i) => i.id)
    out.reestimated = out.reestimated.filter((r) => r.from !== r.to)
    out.prs = uniq(out.prs.reverse(), (p) => `${p.repo}#${p.number}`)
    out.touched = touched.size
    return out
  }, [acts, d])

  const origin = nextWorkday(new Date())
  const finishNow = d.analysis.remaining > 0 ? addWorkdays(origin, Math.ceil(d.analysis.remaining) - 1) : null
  const p85 = d.confidence.p85 > 0 ? addWorkdays(origin, Math.ceil(d.confidence.p85) - 1) : null
  // Baseline: the latest reading taken before the window started (or the first one inside it).
  const sinceDay = toISODate(since)
  const baseline = [...snaps].reverse().find((s) => s.day <= sinceDay) ?? snaps[0]
  const moved = baseline?.finish && finishNow ? workdayDiff(parseDate(baseline.finish), finishNow) : null

  const late = d.work.filter((i) => (d.forecasts.get(i.id)?.daysLate ?? 0) > 0)
  const key = (i: Issue) => d.keyOf(i)

  const copyUpdate = () => {
    if (!digest) return
    const lines = [
      `*${d.project?.name} — standup ${win === 'week' ? '(last 7 days)' : `since ${since.toLocaleDateString(undefined, { weekday: 'long' })}`}*`,
      finishNow
        ? `📅 Forecast: ${formatDate(finishNow)}${moved ? ` (${moved > 0 ? '+' : ''}${moved}d)` : ''} · 85% likely by ${p85 ? formatDate(p85) : '—'}`
        : '📅 Nothing left to schedule',
      digest.shipped.length ? `✅ Shipped: ${digest.shipped.map((i) => `${key(i)} ${i.title}`).join('; ')}` : '',
      digest.unblocked.length ? `🔓 Unblocked: ${digest.unblocked.map((u) => key(u.issue)).join(', ')}` : '',
      digest.started.length ? `▶️ Started: ${digest.started.map(key).join(', ')}` : '',
      d.analysis.atRisk.length ? `⚠️ At risk: ${d.analysis.atRisk.map((id) => key(d.issueMap.get(id)!)).join(', ')}` : '',
      late.length ? `⏰ Forecast late: ${late.map((i) => `${key(i)} (+${d.forecasts.get(i.id)!.daysLate}d)`).join(', ')}` : '',
      d.analysis.topBlockers[0]
        ? `🧱 Biggest blocker: ${key(d.issueMap.get(d.analysis.topBlockers[0].id)!)} (${d.analysis.topBlockers[0].impact} waiting)`
        : '',
    ].filter(Boolean)
    navigator.clipboard.writeText(lines.join('\n')).then(() => toast('Standup copied — paste it into Slack or Teams', 'success'))
  }

  if (!digest)
    return (
      <div className="center-fill">
        <Spinner size={28} />
      </div>
    )

  const IssueRow = ({ issue, right }: { issue: Issue; right?: ReactNode }) => (
    <button className="insight-row" onClick={() => openIssue(key(issue))}>
      <TypeIcon type={issue.type} size={14} />
      <span className="issue-key">{key(issue)}</span>
      <span className="truncate">{issue.title}</span>
      <Avatar user={issue.assigneeId ? d.memberMap.get(issue.assigneeId) : null} size={20} />
      {right !== undefined && <span className="insight-right">{right}</span>}
    </button>
  )

  const reasons: ReactNode[] = []
  const grew = digest.reestimated.filter((r) => r.to > r.from)
  if (digest.added.length)
    reasons.push(
      <>
        <b>{digest.added.length}</b> new {digest.added.length === 1 ? 'issue' : 'issues'} added (+{digest.added.reduce((s, i) => s + i.estimate, 0)}d of work):{' '}
        {digest.added.slice(0, 4).map(key).join(', ')}
      </>,
    )
  if (grew.length)
    reasons.push(
      <>
        <b>{grew.length}</b> re-estimated up: {grew.slice(0, 4).map((r) => `${key(r.issue)} ${r.from}→${r.to}d`).join(', ')}
      </>,
    )
  if (digest.newlyBlocked.length) reasons.push(<><b>{digest.newlyBlocked.length}</b> new dependencies added</>)
  if (digest.shipped.length) reasons.push(<><b>{digest.shipped.length}</b> shipped, pulling the date in</>)

  return (
    <div className="view view-standup">
      <div className="insights-scroll">
        <div className="standup-bar">
          <div className="segmented" role="tablist">
            <button className={win === 'workday' ? 'on' : ''} onClick={() => setWin('workday')}>
              Since last standup
            </button>
            <button className={win === 'week' ? 'on' : ''} onClick={() => setWin('week')}>
              Last 7 days
            </button>
          </div>
          <span className="muted small">
            {digest.touched} issues changed since {since.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' })}
          </span>
          <span className="spacer" />
          <Button icon={<Icon name="copy" size={14} />} onClick={copyUpdate}>
            Copy as update
          </Button>
        </div>

        <section className="panel panel-wide drift">
          <div className="drift-main">
            <span className="muted small">Forecast finish</span>
            <b className="drift-date">{finishNow ? formatDate(finishNow) : 'All done'}</b>
            {moved !== null && (
              <span className={`drift-delta ${moved > 0 ? 'text-danger' : moved < 0 ? 'text-success' : 'muted'}`}>
                {moved === 0 ? 'No change' : `${moved > 0 ? 'Slipped' : 'Pulled in'} ${Math.abs(moved)} working ${Math.abs(moved) === 1 ? 'day' : 'days'}`} since{' '}
                {formatDate(baseline!.day)}
              </span>
            )}
            {p85 && (
              <span className="muted small">
                <Icon name="sparkles" size={12} /> 85% likely by <b>{formatDate(p85)}</b> · 95% by {formatDate(addWorkdays(origin, Math.ceil(d.confidence.p95) - 1))}
              </span>
            )}
          </div>
          <div className="drift-why">
            <h3>What moved the date</h3>
            {reasons.length ? (
              <ul className="reasons">
                {reasons.map((r, k) => (
                  <li key={k}>{r}</li>
                ))}
              </ul>
            ) : (
              <p className="muted">No scope, estimate or dependency changes in this window.</p>
            )}
            <FinishHistory snaps={snaps} />
          </div>
        </section>

        <div className="panel-grid">
          <Panel icon="check-circle" tone="success" title="Shipped" count={digest.shipped.length} empty="Nothing finished yet in this window.">
            {digest.shipped.map((i) => (
              <IssueRow key={i.id} issue={i} />
            ))}
          </Panel>
          <Panel icon="zap" tone="brand" title="Newly unblocked" count={digest.unblocked.length} empty="Nothing was unblocked." note="Ready to pick up because a blocker finished.">
            {digest.unblocked.map((u) => (
              <IssueRow key={u.issue.id} issue={u.issue} right={u.by ? `by ${key(u.by)}` : undefined} />
            ))}
          </Panel>
          <Panel icon="warning" tone="danger" title="Needs attention" count={d.analysis.atRisk.length + digest.newlyBlocked.length + late.length} empty="Nothing is at risk.">
            {d.analysis.atRisk.map((id) => (
              <IssueRow key={`r${id}`} issue={d.issueMap.get(id)!} right={<span className="text-danger">started while blocked</span>} />
            ))}
            {digest.newlyBlocked.map((b) => (
              <IssueRow key={`b${b.issue.id}`} issue={b.issue} right={<span className="text-danger">newly blocked{b.blocker ? ` by ${key(b.blocker)}` : ''}</span>} />
            ))}
            {late.map((i) => (
              <IssueRow key={`l${i.id}`} issue={i} right={<span className="text-danger">+{d.forecasts.get(i.id)!.daysLate}d past due</span>} />
            ))}
          </Panel>
          <Panel icon="board" title="Started" count={digest.started.length} empty="Nothing new in progress.">
            {digest.started.map((i) => (
              <IssueRow key={i.id} issue={i} />
            ))}
          </Panel>
          {digest.prs.length > 0 && (
            <Panel icon="link" title="Pull requests" count={digest.prs.length} empty="">
              {digest.prs.map((p) => (
                <IssueRow key={`${p.repo}#${p.number}`} issue={p.issue} right={<span className={`pr-state pr-${p.state}`}>#{p.number} {p.state}</span>} />
              ))}
            </Panel>
          )}
        </div>
      </div>
    </div>
  )
}

function workdayDiff(a: Date, b: Date) {
  let n = 0
  const step = a < b ? 1 : -1
  for (let x = new Date(a); step > 0 ? x < b : x > b; x.setDate(x.getDate() + step)) {
    const next = new Date(x)
    next.setDate(next.getDate() + step)
    if (next.getDay() !== 0 && next.getDay() !== 6) n += step
  }
  return n
}

function Panel({
  icon,
  tone,
  title,
  count,
  empty,
  note,
  children,
}: {
  icon: 'check-circle' | 'zap' | 'warning' | 'board' | 'link'
  tone?: 'success' | 'danger' | 'brand'
  title: string
  count: number
  empty: string
  note?: string
  children: ReactNode
}) {
  return (
    <section className="panel">
      <header>
        <h3>
          <Icon name={icon} size={16} className={tone ? `tone-${tone}` : ''} /> {title} <span className="count-badge">{count}</span>
        </h3>
        {note && <p className="muted">{note}</p>}
      </header>
      {count === 0 ? <p className="muted">{empty}</p> : children}
    </section>
  )
}

/** Forecast finish over time, one reading per day. A single series, so the heading names it and there's no legend. */
function FinishHistory({ snaps }: { snaps: ForecastSnapshot[] }) {
  const pts = snaps.filter((s) => s.finish)
  if (pts.length < 2)
    return (
      <EmptyState title="">
        <p className="muted small">The finish-date trend appears after a couple of days of readings.</p>
      </EmptyState>
    )
  const t = (s: string) => parseDate(s).getTime()
  const xs = pts.map((p) => t(p.day))
  const ys = pts.map((p) => t(p.finish!))
  const [x0, x1] = [Math.min(...xs), Math.max(...xs)]
  const [y0, y1] = [Math.min(...ys), Math.max(...ys)]
  const W = 520
  const H = 90
  const px = (x: number) => 8 + ((x - x0) / Math.max(1, x1 - x0)) * (W - 16)
  const py = (y: number) => H - 12 - ((y - y0) / Math.max(1, y1 - y0)) * (H - 28)
  const path = pts.map((_, k) => `${k ? 'L' : 'M'}${px(xs[k]).toFixed(1)},${py(ys[k]).toFixed(1)}`).join(' ')
  return (
    <figure className="finish-history">
      <figcaption className="muted small">Forecast finish, by day</figcaption>
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Forecast finish date over time">
        <line x1="8" x2={W - 8} y1={H - 12} y2={H - 12} className="axis" />
        <path d={path} className="line" />
        {pts.map((p, k) => (
          <g key={p.day}>
            <circle cx={px(xs[k])} cy={py(ys[k])} r={10} className="hit">
              <title>{`${formatDate(p.day)}: forecast ${formatDate(p.finish!)}`}</title>
            </circle>
            <circle cx={px(xs[k])} cy={py(ys[k])} r={4} className="dot" />
          </g>
        ))}
        <text x={W} y={py(y1) + 4} className="tick" textAnchor="start" dx="6">
          {formatDate(new Date(y1))}
        </text>
        {y1 !== y0 && (
          <text x={W} y={py(y0) + 4} className="tick" textAnchor="start" dx="6">
            {formatDate(new Date(y0))}
          </text>
        )}
        <text x="8" y={H - 1} className="tick">
          {formatDate(pts[0].day)}
        </text>
        <text x={W - 8} y={H - 1} textAnchor="end" className="tick">
          {formatDate(pts.at(-1)!.day)}
        </text>
      </svg>
    </figure>
  )
}
