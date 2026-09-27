import { useState } from 'react'
import { useProject } from '../store/useProject'
import { ISSUE_TYPES, PRIORITIES, PRIORITY_LABEL, TYPE_LABEL, type Issue, type IssueType, type Priority } from '../types'
import { Icon, PriorityIcon, TypeIcon } from '../ui/icons'
import { Avatar, Lozenge, MenuList, Popover } from '../ui/primitives'

export function StatusButton({ issue, onChange, size = 'md' }: { issue: Issue; onChange: (statusId: string) => void; size?: 'sm' | 'md' }) {
  const { statuses, statusMap } = useProject()
  const cur = statusMap.get(issue.statusId)
  return (
    <Popover
      width={220}
      trigger={({ toggle, ref, open }) => (
        <button
          ref={ref}
          className={`status-btn status-${cur?.category ?? 'todo'} status-btn-${size}${open ? ' open' : ''}`}
          onClick={(e) => {
            e.stopPropagation()
            toggle()
          }}
        >
          {cur?.name ?? 'Unknown'}
          <Icon name="chevron-down" size={14} />
        </button>
      )}
    >
      {(close) => (
        <MenuList
          items={statuses.map((s) => ({ value: s.id, label: <Lozenge category={s.category}>{s.name}</Lozenge> }))}
          selected={issue.statusId}
          onSelect={(v) => {
            close()
            if (v !== issue.statusId) onChange(v)
          }}
        />
      )}
    </Popover>
  )
}

export function AssigneePicker({
  value,
  onChange,
  compact,
}: {
  value: string | null
  onChange: (id: string | null) => void
  compact?: boolean
}) {
  const { members, memberMap } = useProject()
  const cur = value ? memberMap.get(value) : null
  return (
    <Popover
      width={260}
      trigger={({ toggle, ref }) => (
        <button
          ref={ref}
          className={compact ? 'avatar-btn' : 'field-btn'}
          onClick={(e) => {
            e.stopPropagation()
            toggle()
          }}
          title={cur ? `Assignee: ${cur.name}` : 'Unassigned'}
        >
          <Avatar user={cur} size={compact ? 24 : 24} />
          {!compact && <span className={cur ? '' : 'muted'}>{cur?.name ?? 'Unassigned'}</span>}
        </button>
      )}
    >
      {(close) => (
        <MenuList
          searchable
          placeholder="Search people"
          items={[
            { value: '__none', label: 'Unassigned', icon: <Avatar user={null} size={22} /> },
            ...members.map((m) => ({ value: m.id, label: m.name, icon: <Avatar user={m} size={22} />, hint: m.email })),
          ]}
          filterText={(i) => (i.value === '__none' ? 'unassigned' : `${memberMap.get(i.value)?.name} ${memberMap.get(i.value)?.email}`)}
          selected={value ?? '__none'}
          onSelect={(v) => {
            close()
            onChange(v === '__none' ? null : v)
          }}
        />
      )}
    </Popover>
  )
}

export function PriorityPicker({ value, onChange }: { value: Priority; onChange: (p: Priority) => void }) {
  return (
    <Popover
      width={180}
      trigger={({ toggle, ref }) => (
        <button ref={ref} className="field-btn" onClick={toggle}>
          <PriorityIcon priority={value} />
          <span>{PRIORITY_LABEL[value]}</span>
        </button>
      )}
    >
      {(close) => (
        <MenuList
          items={PRIORITIES.map((p) => ({ value: p, label: PRIORITY_LABEL[p], icon: <PriorityIcon priority={p} /> }))}
          selected={value}
          onSelect={(v) => {
            close()
            onChange(v)
          }}
        />
      )}
    </Popover>
  )
}

export function TypePicker({ value, onChange, allowEpic = true, iconOnly }: { value: IssueType; onChange: (t: IssueType) => void; allowEpic?: boolean; iconOnly?: boolean }) {
  return (
    <Popover
      width={180}
      trigger={({ toggle, ref }) => (
        <button ref={ref} className={iconOnly ? 'icon-btn type-btn' : 'field-btn'} onClick={toggle} title={`Type: ${TYPE_LABEL[value]}`}>
          <TypeIcon type={value} />
          {!iconOnly && <span>{TYPE_LABEL[value]}</span>}
          {iconOnly && <Icon name="chevron-down" size={12} />}
        </button>
      )}
    >
      {(close) => (
        <MenuList
          items={ISSUE_TYPES.filter((t) => allowEpic || t !== 'epic').map((t) => ({ value: t, label: TYPE_LABEL[t], icon: <TypeIcon type={t} /> }))}
          selected={value}
          onSelect={(v) => {
            close()
            onChange(v)
          }}
        />
      )}
    </Popover>
  )
}

export function EpicPicker({ value, onChange }: { value: string | null; onChange: (id: string | null) => void }) {
  const { epics, issueMap, keyOf } = useProject()
  const cur = value ? issueMap.get(value) : null
  return (
    <Popover
      width={280}
      trigger={({ toggle, ref }) => (
        <button ref={ref} className="field-btn" onClick={toggle}>
          {cur ? (
            <>
              <TypeIcon type="epic" />
              <span className="truncate">{cur.title}</span>
            </>
          ) : (
            <span className="muted">None</span>
          )}
        </button>
      )}
    >
      {(close) => (
        <MenuList
          searchable
          placeholder="Search epics"
          items={[
            { value: '__none', label: 'None' },
            ...epics.map((e) => ({ value: e.id, label: e.title, icon: <TypeIcon type="epic" />, hint: keyOf(e) })),
          ]}
          filterText={(i) => (i.value === '__none' ? 'none' : `${issueMap.get(i.value)?.title} ${keyOf(issueMap.get(i.value)!)}`)}
          selected={value ?? '__none'}
          onSelect={(v) => {
            close()
            onChange(v === '__none' ? null : v)
          }}
        />
      )}
    </Popover>
  )
}

export function LabelsEditor({ value, onChange }: { value: string[]; onChange: (labels: string[]) => void }) {
  const { issues } = useProject()
  const [draft, setDraft] = useState('')
  const all = [...new Set(issues.flatMap((i) => i.labels))].sort()
  const add = (l: string) => {
    const clean = l.trim().toLowerCase().split(/\s+/).join('-')
    if (clean && !value.includes(clean)) onChange([...value, clean])
    setDraft('')
  }
  return (
    <Popover
      width={240}
      trigger={({ toggle, ref }) => (
        <button ref={ref} className="field-btn labels-btn" onClick={toggle}>
          {value.length ? value.map((l) => <span key={l} className="label-chip">{l}</span>) : <span className="muted">None</span>}
        </button>
      )}
    >
      {() => (
        <div className="labels-editor">
          <div className="labels-current">
            {value.map((l) => (
              <span key={l} className="label-chip removable">
                {l}
                <button onClick={() => onChange(value.filter((x) => x !== l))} aria-label={`Remove ${l}`}>
                  <Icon name="x" size={12} />
                </button>
              </span>
            ))}
          </div>
          <input
            className="menu-search"
            autoFocus
            placeholder="Add a label…"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && add(draft)}
          />
          <div className="menu-items">
            {all
              .filter((l) => !value.includes(l) && l.includes(draft.toLowerCase()))
              .slice(0, 8)
              .map((l) => (
                <button key={l} className="menu-item" onClick={() => add(l)}>
                  <Icon name="tag" size={14} />
                  <span className="menu-label">{l}</span>
                </button>
              ))}
            {draft && !all.includes(draft.toLowerCase()) && (
              <button className="menu-item" onClick={() => add(draft)}>
                <Icon name="plus" size={14} />
                <span className="menu-label">Create “{draft}”</span>
              </button>
            )}
          </div>
        </div>
      )}
    </Popover>
  )
}

/** Choose another issue, e.g. to add as a blocker. `disabled` explains why an option can't be picked. */
export function IssuePicker({
  onPick,
  exclude,
  disabled,
  label,
}: {
  onPick: (id: string) => void
  exclude: Set<string>
  disabled?: (i: Issue) => string | null
  label: string
}) {
  const { work, keyOf, statusMap } = useProject()
  const candidates = work.filter((i) => !exclude.has(i.id))
  return (
    <Popover
      width={380}
      trigger={({ toggle, ref }) => (
        <button ref={ref} className="btn btn-subtle btn-sm" onClick={toggle}>
          <Icon name="plus" size={14} />
          <span>{label}</span>
        </button>
      )}
    >
      {(close) => (
        <MenuList
          searchable
          placeholder="Search by key or summary"
          items={candidates.map((i) => {
            const why = disabled?.(i)
            return {
              value: i.id,
              icon: <TypeIcon type={i.type} />,
              label: (
                <span className={why ? 'muted' : ''}>
                  <b className="issue-key">{keyOf(i)}</b> {i.title}
                </span>
              ),
              hint: why ?? statusMap.get(i.statusId)?.name,
            }
          })}
          filterText={(item) => {
            const i = candidates.find((c) => c.id === item.value)!
            return `${keyOf(i)} ${i.title}`
          }}
          onSelect={(v) => {
            const i = candidates.find((c) => c.id === v)!
            if (disabled?.(i)) return
            close()
            onPick(v)
          }}
        />
      )}
    </Popover>
  )
}
