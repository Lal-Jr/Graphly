import { useMemo, useState } from 'react'
import { FilterBar } from '../components/FilterBar'
import { addWorkdays, formatDate, nextWorkday, parseDate, simulateSlip, workdaysBetween } from '../lib/forecast'
import { useFilters } from '../store/filters'
import { useIssueParam } from '../store/nav'
import { useProject } from '../store/useProject'
import type { Issue } from '../types'
import { Icon, TypeIcon } from '../ui/icons'
import { Avatar, EmptyState, MenuList, Popover } from '../ui/primitives'

const COL = 30 // px per working day

export function TimelineView() {
  const d = useProject()
  const { match } = useFilters()
  const [, openIssue] = useIssueParam()
  const [whatIf, setWhatIf] = useState<{ id: string; slip: number } | null>(null)
  const [byEpic, setByEpic] = useState(true)

  const origin = nextWorkday(new Date())
  const sim = useMemo(() => (whatIf ? simulateSlip(d.nodes, d.dueDates, whatIf.id, whatIf.slip) : null), [whatIf, d.nodes, d.dueDates])
  const delayedBy = new Map(sim?.delayed.map((x) => [x.id, x.by]) ?? [])

  const scheduled = d.work.filter((i) => d.analysis.schedule.has(i.id) && match(i))
  scheduled.sort((a, b) => d.analysis.schedule.get(a.id)!.earliestStart - d.analysis.schedule.get(b.id)!.earliestStart || a.rank - b.rank)

  // Axis runs to the later of the forecast finish (including any simulated slip) and the furthest due date.
  const dueCols = scheduled.filter((i) => i.dueDate).map((i) => workdaysBetween(origin, parseDate(i.dueDate!)))
  const span = Math.max(10, Math.ceil(sim ? sim.after : d.analysis.remaining), ...dueCols) + 3
  const days = Array.from({ length: span }, (_, k) => addWorkdays(origin, k))

  type Row = { kind: 'epic'; epic: Issue | null; items: Issue[] } | { kind: 'issue'; issue: Issue }
  const rows: Row[] = []
  if (byEpic) {
    const groups = new Map<string, Issue[]>()
    for (const i of scheduled) groups.set(i.epicId ?? '', [...(groups.get(i.epicId ?? '') ?? []), i])
    for (const [epicId, items] of groups) {
      rows.push({ kind: 'epic', epic: epicId ? d.issueMap.get(epicId) ?? null : null, items })
      for (const i of items) rows.push({ kind: 'issue', issue: i })
    }
  } else scheduled.forEach((i) => rows.push({ kind: 'issue', issue: i }))

  const finish = d.analysis.remaining > 0 ? addWorkdays(origin, Math.ceil(d.analysis.remaining) - 1) : origin
  const simFinish = sim && sim.after > 0 ? addWorkdays(origin, Math.ceil(sim.after) - 1) : null
  const slipTarget = whatIf ? d.issueMap.get(whatIf.id) : null

  return (
    <div className="view view-timeline">
      <FilterBar>
        <label className="toggle-inline">
          <input type="checkbox" checked={byEpic} onChange={(e) => setByEpic(e.target.checked)} /> Group by epic
        </label>
      </FilterBar>

      <div className="timeline-summary">
        <div className="forecast-card">
          <span className="muted small">If every estimate holds</span>
          <b>{d.analysis.remaining > 0 ? formatDate(finish) : 'All done'}</b>
          <span className="muted small">
            {Math.ceil(d.analysis.remaining)} working days · {d.analysis.criticalPath.length} issues on the critical path
          </span>
        </div>
        {d.analysis.remaining > 0 && (
          <div className="forecast-card confidence-card" title={`From ${d.confidence.runs} simulated runs of the plan with realistic estimate overruns`}>
            <span className="muted small">
              <Icon name="sparkles" size={12} /> Realistic range (estimates overrun)
            </span>
            <div className="confidence-row">
              {(
                [
                  ['50%', d.confidence.p50],
                  ['85%', d.confidence.p85],
                  ['95%', d.confidence.p95],
                ] as const
              ).map(([label, days]) => (
                <span key={label}>
                  <b>{formatDate(addWorkdays(origin, Math.max(0, Math.ceil(days) - 1)))}</b>
                  <span className="muted small">{label} likely</span>
                </span>
              ))}
            </div>
          </div>
        )}
        <div className="whatif">
          <div className="whatif-head">
            <Icon name="sparkles" size={16} />
            <b>What if…</b>
            <Popover
              width={380}
              trigger={({ toggle, ref }) => (
                <button ref={ref} className="btn btn-default btn-sm" onClick={toggle}>
                  {slipTarget ? (
                    <>
                      <TypeIcon type={slipTarget.type} size={14} />
                      <span className="truncate">
                        {d.keyOf(slipTarget)} {slipTarget.title}
                      </span>
                    </>
                  ) : (
                    <span>choose an issue</span>
                  )}
                  <Icon name="chevron-down" size={14} />
                </button>
              )}
            >
              {(close) => (
                <MenuList
                  searchable
                  placeholder="Search open issues"
                  items={scheduled.map((i) => ({
                    value: i.id,
                    icon: <TypeIcon type={i.type} />,
                    label: (
                      <span>
                        <b className="issue-key">{d.keyOf(i)}</b> {i.title}
                      </span>
                    ),
                    hint: d.analysis.criticalSet.has(i.id) ? 'critical' : `${d.analysis.schedule.get(i.id)!.slack}d slack`,
                  }))}
                  filterText={(it) => {
                    const i = d.issueMap.get(it.value)!
                    return `${d.keyOf(i)} ${i.title}`
                  }}
                  onSelect={(v) => {
                    setWhatIf({ id: v, slip: whatIf?.slip ?? 3 })
                    close()
                  }}
                />
              )}
            </Popover>
            <span>slips by</span>
            <input
              type="range"
              min={1}
              max={15}
              value={whatIf?.slip ?? 3}
              disabled={!whatIf}
              onChange={(e) => whatIf && setWhatIf({ ...whatIf, slip: Number(e.target.value) })}
            />
            <b className="slip-days">{whatIf?.slip ?? 3}d</b>
            {whatIf && (
              <button className="icon-btn" onClick={() => setWhatIf(null)} aria-label="Clear simulation">
                <Icon name="x" size={14} />
              </button>
            )}
          </div>
          {sim && simFinish ? (
            <div className="whatif-result">
              {sim.after > sim.before ? (
                <span className="text-danger">
                  Project finish moves <b>{formatDate(finish)} → {formatDate(simFinish)}</b> (+{Math.ceil(sim.after) - Math.ceil(sim.before)} working days).
                </span>
              ) : (
                <span className="text-success">
                  Absorbed by slack — the {formatDate(finish)} finish holds.
                </span>
              )}{' '}
              {sim.delayed.length > 1 && <span>{sim.delayed.length - 1} downstream issues shift. </span>}
              {sim.newlyLate.length > 0 && (
                <span className="text-danger">
                  Newly late: {sim.newlyLate.map((x) => d.keyOf(d.issueMap.get(x)!)).join(', ')}.
                </span>
              )}
            </div>
          ) : (
            <div className="whatif-result muted">Pick an issue to see how a delay ripples through its dependents and due dates.</div>
          )}
        </div>
      </div>

      {scheduled.length === 0 ? (
        <EmptyState icon={<Icon name="timeline" size={32} />} title="Nothing left to schedule">
          <p className="muted">Open issues with estimates appear here, laid out by their dependencies.</p>
        </EmptyState>
      ) : (
        <div className="timeline-scroll">
          <div className="timeline" style={{ gridTemplateColumns: `320px ${span * COL}px` }}>
            <div className="tl-corner">Issue</div>
            <div className="tl-axis">
              {days.map((day, k) => (
                <div key={k} className={`tl-day${day.getDay() === 1 ? ' week-start' : ''}${k === 0 ? ' today' : ''}`} style={{ width: COL }}>
                  {(k === 0 || day.getDay() === 1) && <span className="tl-month">{day.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}</span>}
                  <span className="tl-dow">{'SMTWTFS'[day.getDay()]}</span>
                </div>
              ))}
            </div>

            {rows.map((row, idx) => {
              if (row.kind === 'epic') {
                const starts = row.items.map((i) => d.analysis.schedule.get(i.id)!.earliestStart)
                const ends = row.items.map((i) => d.analysis.schedule.get(i.id)!.earliestFinish)
                const s = Math.min(...starts)
                const e = Math.max(...ends)
                const epicFinish = addWorkdays(origin, Math.max(0, Math.ceil(e) - 1))
                const late = row.epic?.dueDate ? workdaysBetween(parseDate(row.epic.dueDate), epicFinish) : 0
                return (
                  <div key={`e${idx}`} className="tl-row tl-epic-row">
                    <div className="tl-label" onClick={() => row.epic && openIssue(d.keyOf(row.epic))}>
                      {row.epic ? <TypeIcon type="epic" size={14} /> : <Icon name="list" size={14} />}
                      <b className="truncate">{row.epic?.title ?? 'No epic'}</b>
                      <span className={`muted small${late ? ' text-danger' : ''}`}>→ {formatDate(epicFinish)}</span>
                    </div>
                    <div className="tl-track">
                      <div className="tl-epic-bar" style={{ left: Math.floor(s) * COL, width: Math.max(COL / 2, (Math.ceil(e) - Math.floor(s)) * COL) }} />
                    </div>
                  </div>
                )
              }
              const i = row.issue
              const sc = d.analysis.schedule.get(i.id)!
              const fc = d.forecasts.get(i.id)!
              const crit = d.analysis.criticalSet.has(i.id)
              const shift = delayedBy.get(i.id) ?? 0
              const isTarget = whatIf?.id === i.id
              const left = Math.floor(sc.earliestStart) * COL
              const width = Math.max(COL / 2, (Math.ceil(sc.earliestFinish) - Math.floor(sc.earliestStart)) * COL - 4)
              const due = i.dueDate ? workdaysBetween(origin, parseDate(i.dueDate)) : null
              return (
                <div key={i.id} className={`tl-row${d.analysis.blockedBy.has(i.id) ? ' is-blocked' : ''}`}>
                  <div className="tl-label" onClick={() => openIssue(d.keyOf(i))}>
                    <TypeIcon type={i.type} size={14} />
                    <span className="issue-key">{d.keyOf(i)}</span>
                    <span className="truncate">{i.title}</span>
                    <Avatar user={i.assigneeId ? d.memberMap.get(i.assigneeId) : null} size={20} />
                  </div>
                  <div className="tl-track">
                    {shift > 0 && (
                      <div
                        className="tl-ghost"
                        style={{ left: left + (isTarget ? 0 : Math.round(shift) * COL), width: width + (isTarget ? Math.round(shift) * COL : 0) }}
                      />
                    )}
                    <button
                      className={`tl-bar${crit ? ' critical' : ''}${fc.daysLate > 0 ? ' late' : ''}${d.category(i) === 'in_progress' ? ' started' : ''}`}
                      style={{ left, width }}
                      onClick={() => openIssue(d.keyOf(i))}
                      title={`${formatDate(fc.start)} → ${formatDate(fc.finish)} · ${sc.slack}d slack`}
                    >
                      {width >= 24 && <span className="truncate">{i.estimate}d</span>}
                    </button>
                    {sc.slack > 0 && <div className="tl-slack" style={{ left: left + width + 4, width: sc.slack * COL - 4 }} title={`${sc.slack}d slack`} />}
                    {due !== null && due >= 0 && (
                      <div className={`tl-due${fc.daysLate > 0 ? ' late' : ''}`} style={{ left: due * COL + COL / 2 - 6 }} title={`Due ${formatDate(i.dueDate!)}`} />
                    )}
                  </div>
                </div>
              )
            })}
            <div className="tl-today" style={{ left: 320 + COL / 2 }} />
          </div>
        </div>
      )}
    </div>
  )
}
