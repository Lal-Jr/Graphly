export type IssueType = 'story' | 'task' | 'bug' | 'epic'
export type Priority = 'lowest' | 'low' | 'medium' | 'high' | 'highest'
export type Category = 'todo' | 'in_progress' | 'done'

export interface User {
  id: string
  email: string
  name: string
  color: string
}

export interface Workspace {
  id: string
  name: string
  role: 'admin' | 'member'
}

export interface Member extends User {
  role: 'admin' | 'member'
  joinedAt: string
}

export interface Project {
  id: string
  workspaceId: string
  key: string
  name: string
  description: string
  createdAt: string
}

export interface ProjectSummary extends Project {
  openIssues: number
  doneIssues: number
}

export interface Status {
  id: string
  projectId: string
  name: string
  category: Category
  position: number
}

export interface Issue {
  id: string
  projectId: string
  number: number
  type: IssueType
  title: string
  description: string
  statusId: string
  priority: Priority
  assigneeId: string | null
  reporterId: string | null
  estimate: number
  dueDate: string | null
  labels: string[]
  epicId: string | null
  rank: number
  version: number
  createdAt: string
  updatedAt: string
}

export interface Link {
  blockerId: string
  blockedId: string
}

export interface Comment {
  id: string
  issueId: string
  authorId: string | null
  body: string
  createdAt: string
}

export interface Activity {
  id: number
  issueId: string | null
  actorId: string | null
  kind: 'created' | 'updated' | 'deleted' | 'unblocked' | 'blocker.add' | 'blocker.remove' | 'pr'
  data: Record<string, unknown>
  createdAt: string
}

export interface PresenceUser {
  userId: string
  name: string
  color: string
  viewing: string | null
  editing: string | null
}

export interface MyWorkItem extends Issue {
  projectKey: string
  statusName: string
  category: Category
  openBlockers: number
  /** Open issues blocked by this one. */
  waiting: number
}

export interface PullRequest {
  repo: string
  number: number
  title: string
  url: string
  state: 'open' | 'draft' | 'merged' | 'closed'
  author: string
  updatedAt: string
}

export interface ForecastSnapshot {
  day: string
  finish: string | null
  finishP85: string | null
  remaining: number
  openIssues: number
  blocked: number
}

export const ISSUE_TYPES: IssueType[] = ['story', 'task', 'bug', 'epic']
export const PRIORITIES: Priority[] = ['highest', 'high', 'medium', 'low', 'lowest']
export const TYPE_LABEL: Record<IssueType, string> = { story: 'Story', task: 'Task', bug: 'Bug', epic: 'Epic' }
export const PRIORITY_LABEL: Record<Priority, string> = { lowest: 'Lowest', low: 'Low', medium: 'Medium', high: 'High', highest: 'Highest' }
export const PRIORITY_WEIGHT: Record<Priority, number> = { lowest: 0, low: 1, medium: 2, high: 3, highest: 4 }
