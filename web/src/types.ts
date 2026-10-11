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

export interface TypeCalibration {
  source: 'type' | 'pooled' | 'default'
  samples: number
  spread: [number, number]
  median: number
}

/** The overrun model a forecast used: the generic defaults, or ranges learned from the project's own history. */
export interface Calibration {
  source: 'default' | 'calibrated'
  samples: number
  threshold: number
  spreads: Record<string, [number, number]>
  byType: Record<string, TypeCalibration>
}

export interface EstimateQuality {
  openIssues: number
  unestimated: number
  /** Share of open issues with no estimate (0–1). */
  share: number
  confidence: 'high' | 'medium' | 'low'
}

/** The server's forecast (GET /projects/:id/forecast). Dates are YYYY-MM-DD. */
export interface ServerForecast {
  computedAt: string
  today: string
  seed: number
  remaining: number
  finish: string | null
  finishP85: string | null
  p50: number
  p85: number
  p95: number
  spreads: Record<string, [number, number]>
  calibration: Calibration | null
  quality: EstimateQuality | null
}

export type NotificationKind = 'unblocked' | 'late' | 'mention' | 'status' | 'date_moved'

export interface AppNotification {
  id: number
  kind: NotificationKind
  projectId: string | null
  issueId: string | null
  actorId: string | null
  /** A snapshot of what it's about: key, title, projectKey, projectName, actorName, plus kind-specific fields. */
  data: Record<string, unknown>
  createdAt: string
  readAt: string | null
}

export interface ChannelPrefs {
  inApp: boolean
  email: boolean
  slack: boolean
}

export interface NotificationSettings {
  prefs: Record<NotificationKind, ChannelPrefs>
  slackConnected: boolean
  emailAvailable: boolean
}
