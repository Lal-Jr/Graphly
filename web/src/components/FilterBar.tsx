import type { ReactNode } from 'react'
import { useFilters, type QuickFilter } from '../store/filters'
import { useSession } from '../store/session'
import { useProject } from '../store/useProject'
import { ISSUE_TYPES, PRIORITIES, PRIORITY_LABEL, TYPE_LABEL } from '../types'
import { Icon, PriorityIcon, TypeIcon } from '../ui/icons'
import { Avatar, MenuList, Popover } from '../ui/primitives'

const QUICK: { id: QuickFilter; label: string; icon: ReactNode }[] = [
  { id: 'mine', label: 'Mine', icon: <Icon name="user" size={14} /> },
  { id: 'blocked', label: 'Blocked', icon: <Icon name="block" size={14} /> },
  { id: 'critical', label: 'Critical', icon: <Icon name="flame" size={14} /> },
  { id: 'atRisk', label: 'At risk', icon: <Icon name="warning" size={14} /> },
  { id: 'late', label: 'Late', icon: <Icon name="clock" size={14} /> },
]

export function FilterBar({ children }: { children?: ReactNode }) {
  const { filters, set, toggle, clear, active } = useFilters()
  const { members, issues, epics } = useProject()
  const { user } = useSession()
  const labels = [...new Set(issues.flatMap((i) => i.labels))].sort()
  // Show the signed-in user first, like Jira's avatar filter.
  const people = [...members].sort((a, b) => (a.id === user?.id ? -1 : b.id === user?.id ? 1 : 0))

  return (
    <div className="filter-bar">
      <label className="search-field">
        <Icon name="search" size={16} />
        <input placeholder="Search this board" value={filters.q} onChange={(e) => set({ q: e.target.value })} />
      </label>
      <div className="avatar-filter">
        {people.slice(0, 6).map((m) => (
          <button
            key={m.id}
            className={`avatar-toggle${filters.assignees.includes(m.id) ? ' on' : ''}`}
            onClick={() => toggle('assignees', m.id)}
            title={m.name}
          >
            <Avatar user={m} size={30} />
          </button>
        ))}
        <button
          className={`avatar-toggle${filters.assignees.includes('none') ? ' on' : ''}`}
          onClick={() => toggle('assignees', 'none')}
          title="Unassigned"
        >
          <Avatar user={null} size={30} />
        </button>
      </div>

      <FilterMenu label="Type" count={filters.types.length}>
        <MenuList
          items={ISSUE_TYPES.map((t) => ({ value: t, label: TYPE_LABEL[t], icon: <TypeIcon type={t} /> }))}
          selected={filters.types}
          onSelect={(v) => toggle('types', v)}
        />
      </FilterMenu>
      <FilterMenu label="Priority" count={filters.priorities.length}>
        <MenuList
          items={PRIORITIES.map((p) => ({ value: p, label: PRIORITY_LABEL[p], icon: <PriorityIcon priority={p} /> }))}
          selected={filters.priorities}
          onSelect={(v) => toggle('priorities', v)}
        />
      </FilterMenu>
      {epics.length > 0 && (
        <FilterMenu label="Epic" count={filters.epics.length}>
          <MenuList
            searchable
            items={[{ value: 'none', label: 'No epic' }, ...epics.map((e) => ({ value: e.id, label: e.title, icon: <TypeIcon type="epic" /> }))]}
            selected={filters.epics}
            onSelect={(v) => toggle('epics', v)}
            filterText={(i) => String(i.value === 'none' ? 'no epic' : epics.find((e) => e.id === i.value)?.title)}
          />
        </FilterMenu>
      )}
      {labels.length > 0 && (
        <FilterMenu label="Label" count={filters.labels.length}>
          <MenuList
            searchable
            items={labels.map((l) => ({ value: l, label: l, icon: <Icon name="tag" size={14} /> }))}
            selected={filters.labels}
            onSelect={(v) => toggle('labels', v)}
            filterText={(i) => i.value}
          />
        </FilterMenu>
      )}

      <span className="divider-v" />
      {QUICK.map((q) => (
        <button key={q.id} className={`chip${filters.quick.includes(q.id) ? ' on' : ''} chip-${q.id}`} onClick={() => toggle('quick', q.id)} title={q.label}>
          {q.icon}
          <span className="chip-label">{q.label}</span>
        </button>
      ))}
      {active && (
        <button className="btn btn-subtle btn-sm" onClick={clear}>
          Clear filters
        </button>
      )}
      <span className="spacer" />
      {children}
    </div>
  )
}

function FilterMenu({ label, count, children }: { label: string; count: number; children: ReactNode }) {
  return (
    <Popover
      width={240}
      trigger={({ toggle, ref, open }) => (
        <button ref={ref} className={`btn btn-default btn-sm filter-menu${count ? ' has-value' : ''}${open ? ' open' : ''}`} onClick={toggle}>
          <span>{label}</span>
          {count > 0 && <span className="count-badge">{count}</span>}
          <Icon name="chevron-down" size={14} />
        </button>
      )}
    >
      {() => children}
    </Popover>
  )
}
