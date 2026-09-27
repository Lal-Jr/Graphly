export type Status = 'backlog' | 'todo' | 'in_progress' | 'review' | 'done'
export type Priority = 'low' | 'medium' | 'high'

export interface Task {
  id: string
  title: string
  description: string
  status: Status
  priority: Priority
  assignee: string
  /** Estimated effort in days. */
  estimate: number
  /** Ids of tasks that must be done before this one can start. */
  dependsOn: string[]
  /** Sort key within a column. */
  order: number
  createdAt: number
}

export const COLUMNS: { id: Status; label: string }[] = [
  { id: 'backlog', label: 'Backlog' },
  { id: 'todo', label: 'To Do' },
  { id: 'in_progress', label: 'In Progress' },
  { id: 'review', label: 'Review' },
  { id: 'done', label: 'Done' },
]

export const PRIORITIES: Priority[] = ['low', 'medium', 'high']
