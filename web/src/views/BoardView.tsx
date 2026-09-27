import { useMemo, useRef, useState } from 'react'
import { IssueCard, type Relation } from '../components/IssueCard'
import { FilterBar } from '../components/FilterBar'
import { useFilters } from '../store/filters'
import { useIssueParam } from '../store/nav'
import { useProject } from '../store/useProject'
import type { Issue, Status } from '../types'
import { useToast } from '../ui/toast'
import { Icon, StatusGlyph, TypeIcon } from '../ui/icons'
import { Avatar, MenuList, Popover } from '../ui/primitives'

type GroupBy = 'none' | 'assignee' | 'epic'
interface Lane {
  id: string // member/epic id, 'none', or '__all'
  title: string
  icon?: React.ReactNode
}

export function BoardView() {
  const d = useProject()
  const { match } = useFilters()
  const toast = useToast()
  const [, openIssue] = useIssueParam()
  const [groupBy, setGroupBy] = useState<GroupBy>(() => (localStorage.getItem('graphly:groupBy') as GroupBy) || 'none')
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const [hover, setHover] = useState<string | null>(null)
  const [over, setOver] = useState<string | null>(null)
  const dragging = useRef<string | null>(null)

  const visible = d.work.filter(match)

  const related = useMemo(() => {
    const map = new Map<string, Relation>()
    if (!hover) return map
    map.set(hover, 'self')
    const walk = (next: (id: string) => Issue[], tag: Relation) => {
      const stack = next(hover).map((i) => i.id)
      while (stack.length) {
        const id = stack.pop()!
        if (map.has(id)) continue
        map.set(id, tag)
        stack.push(...next(id).map((i) => i.id))
      }
    }
    walk(d.blockersOf, 'upstream')
    walk(d.blocksOf, 'downstream')
    return map
  }, [hover, d])

  const lanes: Lane[] = useMemo(() => {
    if (groupBy === 'assignee') {
      const ids = new Set(visible.map((i) => i.assigneeId ?? 'none'))
      return [
        ...d.members.filter((m) => ids.has(m.id)).map((m) => ({ id: m.id, title: m.name, icon: <Avatar user={m} size={20} /> })),
        ...(ids.has('none') ? [{ id: 'none', title: 'Unassigned', icon: <Avatar user={null} size={20} /> }] : []),
      ]
    }
    if (groupBy === 'epic') {
      const ids = new Set(visible.map((i) => i.epicId ?? 'none'))
      return [
        ...d.epics.filter((e) => ids.has(e.id)).map((e) => ({ id: e.id, title: e.title, icon: <TypeIcon type="epic" /> })),
        ...(ids.has('none') ? [{ id: 'none', title: 'No epic' }] : []),
      ]
    }
    return [{ id: '__all', title: '' }]
  }, [groupBy, visible, d.members, d.epics])

  const laneOf = (i: Issue) => (groupBy === 'assignee' ? i.assigneeId ?? 'none' : groupBy === 'epic' ? i.epicId ?? 'none' : '__all')

  const move = (id: string, status: Status, lane: string, beforeId?: string) => {
    const issue = d.issueMap.get(id)
    if (!issue) return
    // Rank between neighbours in the target cell; ranks are project-wide so this also orders the backlog.
    const cell = d.work.filter((i) => i.statusId === status.id && laneOf(i) === lane && i.id !== id)
    const idx = beforeId ? cell.findIndex((i) => i.id === beforeId) : -1
    let rank: number
    if (idx === -1) rank = (cell.at(-1)?.rank ?? issue.rank) + 1024
    else if (idx === 0) rank = cell[0].rank - 1024
    else rank = (cell[idx - 1].rank + cell[idx].rank) / 2

    const patch: Partial<Issue> = { statusId: status.id, rank }
    if (groupBy === 'assignee' && lane !== laneOf(issue)) patch.assigneeId = lane === 'none' ? null : lane
    if (groupBy === 'epic' && lane !== laneOf(issue)) patch.epicId = lane === 'none' ? null : lane
    void d.store.updateIssue(id, patch)

    const from = d.statusMap.get(issue.statusId)
    const blockers = d.analysis.blockedBy.get(id)
    if (blockers && status.category !== 'todo' && from?.category === 'todo') {
      toast(`${d.keyOf(issue)} is still blocked by ${blockers.map((b) => d.keyOf(d.issueMap.get(b)!)).join(', ')}`, 'warning')
    }
    if (status.category === 'done' && from?.category !== 'done') {
      const freed = (d.analysis.dependents.get(id) ?? []).filter((x) => d.analysis.blockedBy.get(x)?.length === 1)
      if (freed.length) toast(`Unblocked ${freed.map((x) => d.keyOf(d.issueMap.get(x)!)).join(', ')}`, 'success')
    }
  }

  const drop = (status: Status, lane: string, beforeId?: string) => {
    setOver(null)
    if (dragging.current && dragging.current !== beforeId) move(dragging.current, status, lane, beforeId)
    dragging.current = null
  }

  return (
    <div className="view view-board">
      <FilterBar>
        <Popover
          align="end"
          width={200}
          trigger={({ toggle, ref }) => (
            <button ref={ref} className="btn btn-default btn-sm" onClick={toggle}>
              <span>Group: {groupBy === 'none' ? 'None' : groupBy === 'assignee' ? 'Assignee' : 'Epic'}</span>
              <Icon name="chevron-down" size={14} />
            </button>
          )}
        >
          {(close) => (
            <MenuList
              items={[
                { value: 'none', label: 'None' },
                { value: 'assignee', label: 'Assignee' },
                { value: 'epic', label: 'Epic' },
              ]}
              selected={groupBy}
              onSelect={(v) => {
                setGroupBy(v as GroupBy)
                try {
                  localStorage.setItem('graphly:groupBy', v)
                } catch {}
                close()
              }}
            />
          )}
        </Popover>
      </FilterBar>

      <div className="board-scroll">
        <div className="board-grid" style={{ gridTemplateColumns: `repeat(${d.statuses.length}, minmax(260px, 1fr))` }}>
          {d.statuses.map((st) => {
            const all = visible.filter((i) => i.statusId === st.id)
            const days = all.reduce((sum, i) => sum + i.estimate, 0)
            return (
              <div key={st.id} className={`board-col-head cat-${st.category}`}>
                <StatusGlyph category={st.category} />
                <span className="col-name">{st.name}</span>
                <span className="col-count">{all.length}</span>
                <span className="spacer" />
                {days > 0 && <span className="col-days" title="Total estimate">{days}d</span>}
              </div>
            )
          })}
        </div>

        {lanes.map((lane) => {
          const laneIssues = visible.filter((i) => laneOf(i) === lane.id)
          const isCollapsed = collapsed.has(lane.id)
          return (
            <section key={lane.id} className="swimlane">
              {lane.id !== '__all' && (
                <button
                  className="swimlane-head"
                  onClick={() =>
                    setCollapsed((c) => {
                      const n = new Set(c)
                      if (n.has(lane.id)) n.delete(lane.id)
                      else n.add(lane.id)
                      return n
                    })
                  }
                >
                  <Icon name={isCollapsed ? 'chevron-right' : 'chevron-down'} size={16} />
                  {lane.icon}
                  <b>{lane.title}</b>
                  <span className="muted">
                    {laneIssues.length} {laneIssues.length === 1 ? 'issue' : 'issues'}
                  </span>
                </button>
              )}
              {!isCollapsed && (
                <div className="board-grid" style={{ gridTemplateColumns: `repeat(${d.statuses.length}, minmax(260px, 1fr))` }}>
                  {d.statuses.map((st, colIdx) => {
                    const cellKey = `${lane.id}:${st.id}`
                    const cards = laneIssues.filter((i) => i.statusId === st.id)
                    return (
                      <div
                        key={st.id}
                        className={`board-cell${over === cellKey ? ' drop-target' : ''}`}
                        onDragOver={(e) => {
                          e.preventDefault()
                          setOver(cellKey)
                        }}
                        onDragLeave={(e) => {
                          if (!e.currentTarget.contains(e.relatedTarget as Node)) setOver((o) => (o === cellKey ? null : o))
                        }}
                        onDrop={(e) => {
                          e.preventDefault()
                          drop(st, lane.id)
                        }}
                      >
                        {cards.map((i) => (
                          <IssueCard
                            key={i.id}
                            issue={i}
                            relation={related.get(i.id)}
                            onOpen={openIssue}
                            onHover={setHover}
                            onDragStart={(id) => (dragging.current = id)}
                            onDropBefore={(beforeId) => drop(st, lane.id, beforeId)}
                          />
                        ))}
                        {colIdx === 0 && <QuickCreate status={st} lane={lane.id} groupBy={groupBy} />}
                      </div>
                    )
                  })}
                </div>
              )}
            </section>
          )
        })}
        {visible.length === 0 && d.work.length > 0 && <p className="board-empty muted">No issues match these filters.</p>}
      </div>
    </div>
  )
}

function QuickCreate({ status, lane, groupBy }: { status: Status; lane: string; groupBy: GroupBy }) {
  const { store } = useProject()
  const [open, setOpen] = useState(false)
  const [title, setTitle] = useState('')
  if (!open)
    return (
      <button className="quick-create" onClick={() => setOpen(true)}>
        <Icon name="plus" size={16} /> Create issue
      </button>
    )
  const submit = async () => {
    if (!title.trim()) return setOpen(false)
    const fields: Record<string, unknown> = { title: title.trim(), statusId: status.id }
    if (groupBy === 'assignee' && lane !== 'none') fields.assigneeId = lane
    if (groupBy === 'epic' && lane !== 'none') fields.epicId = lane
    setTitle('')
    await store.createIssue(fields as { title: string })
  }
  return (
    <form
      className="quick-create-form"
      onSubmit={(e) => {
        e.preventDefault()
        void submit()
      }}
    >
      <textarea
        autoFocus
        rows={2}
        placeholder="What needs to be done?"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        onBlur={() => !title.trim() && setOpen(false)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault()
            void submit()
          } else if (e.key === 'Escape') setOpen(false)
        }}
      />
      <span className="muted small">Enter to create · Esc to cancel</span>
    </form>
  )
}
