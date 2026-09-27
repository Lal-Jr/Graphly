import type { ReactNode } from 'react'
import { addWorkdays, formatDate, nextWorkday, parseDate, workdaysBetween } from '../lib/forecast'
import { useIssueParam } from '../store/nav'
import { useProject } from '../store/useProject'
import { PRIORITY_WEIGHT, type Issue } from '../types'
import { Icon, TypeIcon } from '../ui/icons'
import { Avatar, Lozenge } from '../ui/primitives'

const STALE_DAYS = 3
/** More in-progress issues than this per person is flagged as context switching. */
const WIP_LIMIT = 2
const daysSince = (iso: string) => Math.floor((Date.now() - new Date(iso).getTime()) / 86_400_000)

export function InsightsView() {
  const d = useProject()
  const [, openIssue] = useIssueParam()
  const a = d.analysis
  const origin = nextWorkday(new Date())
  const open = d.work.filter((i) => d.category(i) !== 'done')
  const late = open.filter((i) => (d.forecasts.get(i.id)?.daysLate ?? 0) > 0).sort((x, y) => d.forecasts.get(y.id)!.daysLate - d.forecasts.get(x.id)!.daysLate)
  const finish = a.remaining > 0 ? addWorkdays(origin, Math.ceil(a.remaining) - 1) : null
  // Dated issues under 85% likely to land on time, riskiest first.
  const risky = open
    .filter((i) => d.confidence.onTime.has(i.id) && d.confidence.onTime.get(i.id)! < 0.85)
    .map((i) => ({ issue: i, odds: d.confidence.onTime.get(i.id)! }))
    .sort((x, y) => x.odds - y.odds)
  const stale = a.topBlockers.map((b) => d.issueMap.get(b.id)!).filter((i) => daysSince(i.updatedAt) >= STALE_DAYS)

  // Per-person load: open estimate, split into critical-path and other work, plus downstream work they hold up.
  const people = [...d.members, null].map((m) => {
    const mine = open.filter((i) => i.assigneeId === (m?.id ?? null))
    const critical = mine.filter((i) => a.criticalSet.has(i.id)).reduce((s, i) => s + i.estimate, 0)
    const total = mine.reduce((s, i) => s + i.estimate, 0)
    // Distinct open issues downstream of any of this person's work (an issue behind two of theirs counts once).
    const downstream = new Set<string>()
    const stack = mine.flatMap((i) => a.dependents.get(i.id) ?? [])
    while (stack.length) {
      const id = stack.pop()!
      if (downstream.has(id)) continue
      downstream.add(id)
      stack.push(...(a.dependents.get(id) ?? []))
    }
    const holding = downstream.size
    // Next best task: unblocked, not started; critical first, then most downstream impact, then priority.
    const next = mine
      .filter((i) => a.ready.includes(i.id))
      .sort(
        (x, y) =>
          Number(a.criticalSet.has(y.id)) - Number(a.criticalSet.has(x.id)) ||
          (a.impact.get(y.id) ?? 0) - (a.impact.get(x.id) ?? 0) ||
          PRIORITY_WEIGHT[y.priority] - PRIORITY_WEIGHT[x.priority],
      )[0]
    const wip = mine.filter((i) => d.category(i) === 'in_progress').length
    return { member: m, count: mine.length, critical, other: total - critical, total, holding, next, wip }
  }).filter((p) => p.count > 0)
  const maxLoad = Math.max(1, ...people.map((p) => p.total))

  const key = (i: Issue) => d.keyOf(i)
  const IssueLink = ({ issue, right }: { issue: Issue; right?: ReactNode }) => (
    <button className="insight-row" onClick={() => openIssue(key(issue))}>
      <TypeIcon type={issue.type} size={14} />
      <span className="issue-key">{key(issue)}</span>
      <span className="truncate">{issue.title}</span>
      {right !== undefined && <span className="insight-right">{right}</span>}
    </button>
  )

  return (
    <div className="view view-insights">
      <div className="insights-scroll">
        <div className="insights-hero">
          <ShipDate />
          <div className="kpis">
            <Kpi label="Open issues" value={open.length} sub={`${d.work.length - open.length} done · ${a.ready.length} ready to start`} />
            <Kpi label="Blocked" value={a.blockedBy.size} tone={a.blockedBy.size ? 'danger' : undefined} icon="block" sub="waiting on open work" />
            <Kpi label="At risk" value={a.atRisk.length} tone={a.atRisk.length ? 'danger' : undefined} icon="warning" sub="started while blocked" />
            <Kpi label="Forecast late" value={late.length} tone={late.length ? 'danger' : undefined} icon="clock" sub="will miss their due date" />
          </div>
        </div>

        <section className="panel panel-wide">
          <header>
            <h3>
              <Icon name="route" size={16} className="text-critical" /> Critical path
            </h3>
            <p className="muted">The longest chain of dependent work. Any slip on this line moves the ship date; everything off it has slack.</p>
          </header>
          {a.criticalPath.length ? (
            <ol className="cp-line">
              {a.criticalPath.map((id) => {
                const i = d.issueMap.get(id)!
                const fc = d.forecasts.get(id)!
                const started = d.category(i) === 'in_progress'
                return (
                  <li key={id} className={`cp-stop${started ? ' is-started' : ''}`}>
                    <button onClick={() => openIssue(key(i))} title={i.title}>
                      <span className="cp-when">{formatDate(fc.finish)}</span>
                      <span className="cp-dot" />
                      <span className="cp-top">
                        <span className="issue-key">{key(i)}</span>
                        <Avatar user={i.assigneeId ? d.memberMap.get(i.assigneeId) : null} size={18} />
                      </span>
                      <span className="cp-title">{i.title}</span>
                      <span className="muted small">
                        {i.estimate}d{started ? ' · in progress' : ''}
                      </span>
                    </button>
                  </li>
                )
              })}
              <li className="cp-stop cp-end">
                <span className="cp-when">{finish ? formatDate(finish) : ''}</span>
                <span className="cp-dot" />
                <span className="cp-top">
                  <Icon name="target" size={14} /> Ship
                </span>
              </li>
            </ol>
          ) : (
            <p className="muted">Nothing left to schedule.</p>
          )}
        </section>

        <div className="panel-grid">
          <section className="panel">
            <header>
              <h3>Top blockers</h3>
              <p className="muted">Open issues holding up the most downstream work.</p>
            </header>
            {a.topBlockers.slice(0, 6).map(({ id, impact }) => (
              <IssueLink key={id} issue={d.issueMap.get(id)!} right={<b>{impact} waiting</b>} />
            ))}
            {a.topBlockers.length === 0 && <p className="muted">No open dependencies.</p>}
          </section>

          <section className="panel">
            <header>
              <h3>Due-date odds</h3>
              <p className="muted">Chance each dated issue lands on time, from simulating the plan with realistic overruns.</p>
            </header>
            {risky.slice(0, 6).map(({ issue: i, odds }) => (
              <IssueLink
                key={i.id}
                issue={i}
                right={
                  <span className={odds < 0.5 ? 'text-danger' : ''}>
                    <span className={`odds ${odds >= 0.85 ? 'good' : odds >= 0.5 ? 'fair' : 'poor'}`}>{Math.round(odds * 100)}%</span> · due {formatDate(i.dueDate!)}
                  </span>
                }
              />
            ))}
            {risky.length === 0 && (
              <p className="muted">
                <Icon name="check-circle" size={14} className="text-success" /> Everything with a due date is on track.
              </p>
            )}
          </section>

          <section className="panel">
            <header>
              <h3>Stale blockers</h3>
              <p className="muted">Blocking others but untouched for {STALE_DAYS}+ days — worth a nudge.</p>
            </header>
            {stale.slice(0, 6).map((i) => (
              <IssueLink key={i.id} issue={i} right={`${daysSince(i.updatedAt)}d idle`} />
            ))}
            {stale.length === 0 && <p className="muted">All blockers are moving.</p>}
          </section>

          <section className="panel">
            <header>
              <h3>At risk</h3>
              <p className="muted">Work in progress whose blockers aren't done yet.</p>
            </header>
            {a.atRisk.map((id) => (
              <IssueLink key={id} issue={d.issueMap.get(id)!} right={`waiting on ${a.blockedBy.get(id)!.map((b) => key(d.issueMap.get(b)!)).join(', ')}`} />
            ))}
            {a.atRisk.length === 0 && <p className="muted">Nothing started out of order.</p>}
          </section>
        </div>

        <section className="panel panel-wide">
          <header>
            <h3>People &amp; bottlenecks</h3>
            <p className="muted">Open work per person, how much is on the critical path, who's context switching, and what each person should pick up next.</p>
          </header>
          <div className="legend">
            <span>
              <i className="swatch swatch-critical" /> Critical path
            </span>
            <span>
              <i className="swatch swatch-other" /> Other open work
            </span>
          </div>
          <table className="people-table">
            <thead>
              <tr>
                <th>Person</th>
                <th className="load-col">Open work (days)</th>
                <th className="num" title="Issues in progress at once. More than 2 means context switching.">In progress</th>
                <th className="num">Holding up</th>
                <th>Up next</th>
              </tr>
            </thead>
            <tbody>
              {people.map((p) => (
                <tr key={p.member?.id ?? 'none'}>
                  <td>
                    <span className="person">
                      <Avatar user={p.member} size={24} />
                      {p.member?.name ?? 'Unassigned'}
                    </span>
                  </td>
                  <td>
                    <div className="load-bar" title={`${p.critical}d critical path · ${p.other}d other · ${p.count} issues`}>
                      {p.critical > 0 && <span className="seg seg-critical" style={{ width: `${(p.critical / maxLoad) * 100}%` }} />}
                      {p.other > 0 && <span className="seg seg-other" style={{ width: `${(p.other / maxLoad) * 100}%` }} />}
                      <span className="load-value">{p.total}d</span>
                    </div>
                  </td>
                  <td className="num">
                    {p.wip > WIP_LIMIT ? (
                      <span className="signal signal-danger" title="Juggling this many in-progress issues usually means context switching. Finish one before starting another.">
                        <Icon name="warning" size={12} /> {p.wip} · overloaded
                      </span>
                    ) : (
                      p.wip || <span className="muted">—</span>
                    )}
                  </td>
                  <td className="num">{p.holding > 0 ? <b>{p.holding} issues</b> : <span className="muted">—</span>}</td>
                  <td>
                    {p.next ? (
                      <button className="link-btn" onClick={() => openIssue(key(p.next!))}>
                        <span className="issue-key">{key(p.next)}</span> <span className="truncate">{p.next.title}</span>
                      </button>
                    ) : (
                      <span className="muted">Nothing ready</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>

        {d.epics.length > 0 && (
          <section className="panel panel-wide">
            <header>
              <h3>Epic forecasts</h3>
              <p className="muted">Each epic finishes when its last child does — computed, not guessed.</p>
            </header>
            <div className="epic-list">
              {d.epics.map((e) => {
                const kids = d.childrenOf(e.id)
                const doneKids = kids.filter((k) => d.category(k) === 'done')
                const total = kids.reduce((s, k) => s + k.estimate, 0)
                const doneDays = doneKids.reduce((s, k) => s + k.estimate, 0)
                const finishes = kids.map((k) => d.forecasts.get(k.id)?.finish).filter(Boolean) as Date[]
                const efinish = finishes.length ? new Date(Math.max(...finishes.map((f) => f.getTime()))) : null
                const lateBy = e.dueDate && efinish ? workdaysBetween(parseDate(e.dueDate), efinish) : 0
                const pct = total ? Math.round((doneDays / total) * 100) : kids.length ? Math.round((doneKids.length / kids.length) * 100) : 0
                return (
                  <button key={e.id} className="epic-row" onClick={() => openIssue(key(e))}>
                    <TypeIcon type="epic" />
                    <span className="epic-name">
                      <b className="truncate">{e.title}</b>
                      <span className="muted small">
                        {doneKids.length}/{kids.length} issues · {doneDays}/{total}d
                      </span>
                    </span>
                    <span className="meter" title={`${pct}% of estimated work done`}>
                      <span style={{ width: `${pct}%` }} />
                    </span>
                    <span className="epic-pct">{pct}%</span>
                    <span className="epic-when">
                      {efinish ? (
                        <span className={lateBy > 0 ? 'text-danger' : ''}>
                          {formatDate(efinish)}
                          {lateBy > 0 && ` · ${lateBy}d late`}
                        </span>
                      ) : kids.length && doneKids.length === kids.length ? (
                        <Lozenge category="done">Complete</Lozenge>
                      ) : (
                        <span className="muted">—</span>
                      )}
                    </span>
                  </button>
                )
              })}
            </div>
          </section>
        )}
      </div>
    </div>
  )
}

/** The headline: the ship date if estimates hold, and the spread of 1000 simulated runs around it. */
function ShipDate() {
  const d = useProject()
  const a = d.analysis
  const c = d.confidence
  const origin = nextWorkday(new Date())
  const at = (days: number) => addWorkdays(origin, Math.max(1, Math.ceil(days)) - 1)
  if (a.remaining <= 0)
    return (
      <section className="ship">
        <span className="eyebrow">Ships</span>
        <b className="ship-date">Done</b>
        <p className="muted">Nothing left open on this project.</p>
      </section>
    )

  // Due date of the project: the latest due date on any open issue, if there is one.
  const dues = d.work.filter((i) => i.dueDate && d.category(i) !== 'done').map((i) => i.dueDate!)
  const due = dues.length ? dues.sort().at(-1)! : null
  const dueDay = due ? workdaysBetween(origin, parseDate(due)) + 1 : null

  const h = c.histogram
  const W = 460
  const H = 120
  const maxDay = Math.max(h.length - 1, Math.ceil(c.p95) + 1, dueDay ?? 0)
  const minDay = Math.max(0, Math.min(Math.ceil(a.remaining) - 2, h.findIndex((n) => n > 0) - 1, (dueDay ?? Infinity) - 1))
  const span = Math.max(1, maxDay - minDay)
  const peak = Math.max(1, ...h)
  const x = (day: number) => ((day - minDay) / span) * W
  const y = (n: number) => H - 4 - (n / peak) * (H - 16)
  const pts = Array.from({ length: span + 1 }, (_, k) => [x(minDay + k), y(h[minDay + k] ?? 0)] as const)
  const line = pts.map(([px, py], k) => `${k ? 'L' : 'M'}${px.toFixed(1)},${py.toFixed(1)}`).join('')
  const area = `${line}L${W},${H}L0,${H}Z`
  const marks = [
    { day: Math.ceil(c.p50), label: '50%' },
    { day: Math.ceil(c.p85), label: '85%' },
    { day: Math.ceil(c.p95), label: '95%' },
  ]
  const late = dueDay !== null && Math.ceil(c.p85) > dueDay

  return (
    <section className="ship">
      <div className="ship-copy">
        <span className="eyebrow">
          <Icon name="target" size={13} /> Ships
        </span>
        <b className="ship-date">{formatDate(at(a.remaining))}</b>
        <p className="ship-sub">
          if every estimate holds · <b>{Math.ceil(a.remaining)}</b> working days
        </p>
        <div className="ship-odds">
          {marks.map((m) => (
            <span key={m.label} className={m.label === '85%' ? 'is-key' : ''}>
              <b>{formatDate(at(m.day))}</b>
              <span>{m.label} likely</span>
            </span>
          ))}
        </div>
        {due && (
          <p className={`ship-due${late ? ' is-late' : ''}`}>
            {late ? <Icon name="warning" size={13} /> : <Icon name="check-circle" size={13} />}
            Due {formatDate(due)} · {Math.round((h.slice(0, (dueDay ?? 0) + 1).reduce((s, n) => s + n, 0) / c.runs) * 100)}% of runs make it
          </p>
        )}
      </div>
      <svg className="ship-curve" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" role="img" aria-label={`Simulated finish dates: 50% by ${formatDate(at(c.p50))}, 85% by ${formatDate(at(c.p85))}`}>
        <defs>
          <linearGradient id="ship-fill" x1="0" x2="0" y1="0" y2="1">
            <stop offset="0" stopColor="var(--accent)" stopOpacity=".35" />
            <stop offset="1" stopColor="var(--accent)" stopOpacity="0" />
          </linearGradient>
        </defs>
        <path d={area} fill="url(#ship-fill)" />
        <path d={line} fill="none" stroke="var(--accent)" strokeWidth="2" vectorEffect="non-scaling-stroke" />
        {marks.map((m) => (
          <line key={m.label} x1={x(m.day)} x2={x(m.day)} y1={6} y2={H} className={`ship-mark${m.label === '85%' ? ' is-key' : ''}`} vectorEffect="non-scaling-stroke" />
        ))}
        {dueDay !== null && dueDay >= minDay && (
          <line x1={x(dueDay)} x2={x(dueDay)} y1={0} y2={H} className="ship-due-line" vectorEffect="non-scaling-stroke" />
        )}
      </svg>
      <p className="ship-foot muted small">
        {c.runs.toLocaleString()} simulated runs with realistic overruns
        {' · dotted lines at 50 / 85 / 95%'}
        {dueDay !== null && ' · coral line is the due date'}
      </p>
    </section>
  )
}

function Kpi({ label, value, sub, tone, icon }: { label: string; value: ReactNode; sub: string; tone?: 'danger'; icon?: 'block' | 'warning' | 'clock' }) {
  return (
    <div className={`kpi${tone ? ` kpi-${tone}` : ''}`}>
      <span className="kpi-label">
        {icon && tone && <Icon name={icon} size={14} />}
        {label}
      </span>
      <b className="kpi-value">{value}</b>
      <span className="kpi-sub">{sub}</span>
    </div>
  )
}
