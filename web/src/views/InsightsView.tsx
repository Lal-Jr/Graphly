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
        <div className="kpis">
          <Kpi label="If estimates hold" value={finish ? formatDate(finish) : 'Done'} sub={`${Math.ceil(a.remaining)} working days left`} />
          <Kpi
            label="85% confident by"
            value={a.remaining > 0 ? formatDate(addWorkdays(origin, Math.ceil(d.confidence.p85) - 1)) : '—'}
            sub={`${d.confidence.runs} simulated runs`}
          />
          <Kpi label="Open issues" value={open.length} sub={`${d.work.length - open.length} done`} />
          <Kpi label="Blocked" value={a.blockedBy.size} tone={a.blockedBy.size ? 'danger' : undefined} icon="block" sub={`${a.ready.length} ready to start`} />
          <Kpi label="At risk" value={a.atRisk.length} tone={a.atRisk.length ? 'danger' : undefined} icon="warning" sub="started while blocked" />
          <Kpi label="Forecast late" value={late.length} tone={late.length ? 'danger' : undefined} icon="clock" sub="will miss due date" />
        </div>

        <section className="panel panel-wide">
          <header>
            <h3>
              <Icon name="flame" size={16} className="text-critical" /> Critical path
            </h3>
            <p className="muted">The longest chain of dependent work. Any slip here moves the finish date; everything else has slack.</p>
          </header>
          {a.criticalPath.length ? (
            <div className="cp-chain">
              {a.criticalPath.map((id, k) => {
                const i = d.issueMap.get(id)!
                const fc = d.forecasts.get(id)!
                return (
                  <div key={id} className="cp-step">
                    {k > 0 && <Icon name="arrow-right" size={16} className="cp-arrow" />}
                    <button className="cp-node" onClick={() => openIssue(key(i))}>
                      <span className="cp-top">
                        <TypeIcon type={i.type} size={14} />
                        <span className="issue-key">{key(i)}</span>
                        <Avatar user={i.assigneeId ? d.memberMap.get(i.assigneeId) : null} size={18} />
                      </span>
                      <span className="cp-title">{i.title}</span>
                      <span className="muted small">
                        {i.estimate}d · done {formatDate(fc.finish)}
                      </span>
                    </button>
                  </div>
                )
              })}
            </div>
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
