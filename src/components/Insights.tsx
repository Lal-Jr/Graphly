import type { ReactNode } from 'react'
import { useBoard } from '../store/context'

export function Insights({ onOpen }: { onOpen: (id: string) => void }) {
  const { tasks, byId, analysis } = useBoard()
  const open = tasks.filter((t) => t.status !== 'done')
  const done = tasks.length - open.length
  const pct = tasks.length ? Math.round((done / tasks.length) * 100) : 0

  const item = (id: string, right?: ReactNode) => {
    const t = byId.get(id)
    if (!t) return null
    return (
      <li key={id}>
        <button className="link" onClick={() => onOpen(id)}>
          <span className={`dot s-${t.status}`} />
          <span className="truncate">{t.title || 'Untitled'}</span>
          {right !== undefined && <span className="right">{right}</span>}
        </button>
      </li>
    )
  }

  return (
    <aside className="insights">
      <div className="stats">
        <Stat label="Days remaining" value={analysis.remaining} hint="Length of the critical path" />
        <Stat label="Blocked" value={analysis.blockedBy.size} tone={analysis.blockedBy.size ? 'danger' : undefined} />
        <Stat label="Ready" value={analysis.ready.length} tone="ok" />
        <Stat label="Done" value={`${pct}%`} />
      </div>
      <div className="progress" aria-label={`${pct}% done`}>
        <span style={{ width: `${pct}%` }} />
      </div>

      {analysis.inCycle.size > 0 && (
        <Section title="⟳ Dependency cycles" tone="danger" note="These tasks wait on each other and can never start.">
          {[...analysis.inCycle].map((id) => item(id))}
        </Section>
      )}

      {analysis.atRisk.length > 0 && (
        <Section title="⚠ At risk" tone="danger" note="In progress, but a dependency is still open.">
          {analysis.atRisk.map((id) => item(id, `${analysis.blockedBy.get(id)!.length} open`))}
        </Section>
      )}

      <Section title="◆ Critical path" tone="crit" note={analysis.criticalPath.length ? `Any delay here slips the ${analysis.remaining}-day finish.` : undefined}>
        {analysis.criticalPath.map((id) => item(id, `${byId.get(id)!.estimate}d`))}
        {analysis.criticalPath.length === 0 && <li className="muted">Nothing left to schedule 🎉</li>}
      </Section>

      <Section title="⤳ Top blockers" tone="warn" note="Open tasks holding up the most downstream work.">
        {analysis.topBlockers.slice(0, 5).map(({ id, impact }) => item(id, `blocks ${impact}`))}
        {analysis.topBlockers.length === 0 && <li className="muted">No blockers</li>}
      </Section>

      <Section title="✓ Ready to start">
        {analysis.ready.slice(0, 6).map((id) => item(id, byId.get(id)!.assignee || '—'))}
        {analysis.ready.length === 0 && <li className="muted">Nothing unblocked in the queue</li>}
      </Section>
    </aside>
  )
}

function Stat({ label, value, hint, tone }: { label: string; value: ReactNode; hint?: string; tone?: string }) {
  return (
    <div className={`stat ${tone ?? ''}`} title={hint}>
      <b>{value}</b>
      <span>{label}</span>
    </div>
  )
}

function Section({ title, note, tone, children }: { title: string; note?: string; tone?: string; children: ReactNode }) {
  return (
    <section className={`insight ${tone ?? ''}`}>
      <h3>{title}</h3>
      {note && <p className="muted">{note}</p>}
      <ul>{children}</ul>
    </section>
  )
}
