import { useState } from 'react'
import { AssigneePicker, StatusButton } from '../components/fields'
import { FilterBar } from '../components/FilterBar'
import { formatDate } from '../lib/forecast'
import { useFilters } from '../store/filters'
import { useIssueParam } from '../store/nav'
import { useProject } from '../store/useProject'
import { PRIORITY_WEIGHT, type Issue } from '../types'
import { Icon, PriorityIcon, TypeIcon } from '../ui/icons'
import { EmptyState } from '../ui/primitives'

type SortKey = 'rank' | 'key' | 'title' | 'status' | 'assignee' | 'priority' | 'estimate' | 'due' | 'forecast' | 'slack'

export function ListView() {
  const d = useProject()
  const { match } = useFilters()
  const [, openIssue] = useIssueParam()
  const [sort, setSort] = useState<{ key: SortKey; dir: 1 | -1 }>({ key: 'rank', dir: 1 })
  const [showEpics, setShowEpics] = useState(true)

  const rows = d.issues.filter((i) => (showEpics || i.type !== 'epic') && match(i))
  const val = (i: Issue): number | string => {
    switch (sort.key) {
      case 'rank':
        return i.rank
      case 'key':
        return i.number
      case 'title':
        return i.title.toLowerCase()
      case 'status':
        return d.statusMap.get(i.statusId)?.position ?? 0
      case 'assignee':
        return d.memberMap.get(i.assigneeId ?? '')?.name.toLowerCase() ?? '￿'
      case 'priority':
        return -PRIORITY_WEIGHT[i.priority]
      case 'estimate':
        return i.estimate
      case 'due':
        return i.dueDate ?? '9999'
      case 'forecast':
        return d.forecasts.get(i.id)?.finish.getTime() ?? Number.MAX_SAFE_INTEGER
      case 'slack':
        return d.analysis.schedule.get(i.id)?.slack ?? Number.MAX_SAFE_INTEGER
    }
  }
  rows.sort((a, b) => {
    const x = val(a)
    const y = val(b)
    return (x < y ? -1 : x > y ? 1 : 0) * sort.dir
  })

  const Th = ({ k, children, className = '' }: { k: SortKey; children: React.ReactNode; className?: string }) => (
    <th className={className}>
      <button className={`th-sort${sort.key === k ? ' active' : ''}`} onClick={() => setSort((s) => ({ key: k, dir: s.key === k ? ((-s.dir) as 1 | -1) : 1 }))}>
        {children}
        {sort.key === k && <Icon name="chevron-down" size={12} className={sort.dir === -1 ? 'flip' : ''} />}
      </button>
    </th>
  )

  return (
    <div className="view view-list">
      <FilterBar>
        <label className="toggle-inline">
          <input type="checkbox" checked={showEpics} onChange={(e) => setShowEpics(e.target.checked)} /> Show epics
        </label>
      </FilterBar>
      <div className="table-wrap">
        {rows.length === 0 ? (
          <EmptyState icon={<Icon name="list" size={32} />} title="No issues match">
            <p className="muted">Try clearing some filters.</p>
          </EmptyState>
        ) : (
          <table className="issue-table">
            <thead>
              <tr>
                <th className="col-type" />
                <Th k="key" className="col-key">Key</Th>
                <Th k="title">Summary</Th>
                <Th k="status">Status</Th>
                <Th k="assignee">Assignee</Th>
                <Th k="priority">Priority</Th>
                <Th k="estimate" className="num">Est.</Th>
                <Th k="due">Due</Th>
                <Th k="forecast">Forecast</Th>
                <Th k="slack" className="num">Slack</Th>
                <th>Blocked by</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((i) => {
                const fc = d.forecasts.get(i.id)
                const sched = d.analysis.schedule.get(i.id)
                const blockers = d.analysis.blockedBy.get(i.id) ?? []
                const done = d.category(i) === 'done'
                return (
                  <tr key={i.id} onClick={() => openIssue(d.keyOf(i))} className={done ? 'is-done' : ''}>
                    <td>
                      <TypeIcon type={i.type} />
                    </td>
                    <td className="issue-key">{d.keyOf(i)}</td>
                    <td className="col-title">
                      <span className="truncate">{i.title}</span>
                      {d.analysis.criticalSet.has(i.id) && (
                        <span className="signal signal-critical" title="Critical path">
                          <Icon name="flame" size={12} />
                        </span>
                      )}
                    </td>
                    <td onClick={(e) => e.stopPropagation()}>
                      <StatusButton issue={i} size="sm" onChange={(statusId) => d.store.updateIssue(i.id, { statusId })} />
                    </td>
                    <td onClick={(e) => e.stopPropagation()}>
                      <AssigneePicker value={i.assigneeId} onChange={(assigneeId) => d.store.updateIssue(i.id, { assigneeId })} />
                    </td>
                    <td>
                      <span className="cell-icon">
                        <PriorityIcon priority={i.priority} />
                      </span>
                    </td>
                    <td className="num">{i.type === 'epic' ? '—' : `${i.estimate}d`}</td>
                    <td>{i.dueDate ? formatDate(i.dueDate) : <span className="muted">—</span>}</td>
                    <td>
                      {fc ? (
                        <span className={fc.daysLate > 0 ? 'text-danger' : ''} title={fc.daysLate > 0 ? `${fc.daysLate} working days after the due date` : undefined}>
                          {formatDate(fc.finish)}
                          {fc.daysLate > 0 && ` (+${fc.daysLate}d)`}
                        </span>
                      ) : (
                        <span className="muted">{done ? 'Done' : '—'}</span>
                      )}
                    </td>
                    <td className="num">{sched ? (sched.slack === 0 ? <b className="text-critical">0</b> : `${sched.slack}d`) : '—'}</td>
                    <td>
                      {blockers.length > 0 ? (
                        <span className="blocker-keys">{blockers.map((b) => d.keyOf(d.issueMap.get(b)!)).join(', ')}</span>
                      ) : (
                        <span className="muted">—</span>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}
