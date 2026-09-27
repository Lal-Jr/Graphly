import { api, ApiError, errorMessage } from '../api'
import type { Activity, Comment, Issue, Link, Member, PresenceUser, Project, PullRequest, Status } from '../types'

export interface ProjectState {
  phase: 'loading' | 'ready' | 'error'
  error?: string
  /** The project was deleted (by us or someone else). */
  deleted: boolean
  connected: boolean
  project: Project | null
  statuses: Status[]
  issues: Issue[]
  links: Link[]
  members: Member[]
  presence: PresenceUser[]
}

interface Snapshot {
  project: Project
  statuses: Status[]
  issues: Issue[]
  links: Link[]
  members: Member[]
  seq: number
}

type ServerEvent =
  | { type: 'issue.upsert'; issue: Issue }
  | { type: 'issue.delete'; id: string }
  | { type: 'link.add'; link: Link }
  | { type: 'link.remove'; link: Link }
  | { type: 'statuses.update'; statuses: Status[] }
  | { type: 'project.update'; project: Project }
  | { type: 'project.delete'; id: string }
  | { type: 'member.join'; member: Member }
  | { type: 'comment.add'; comment: Comment }
  | { type: 'comment.delete'; id: string; issueId: string }
  | { type: 'activity.add'; activity: Activity }
  | { type: 'pr.update'; issueId: string; pr: PullRequest }

export type FeedEvent = Extract<ServerEvent, { type: 'comment.add' | 'comment.delete' | 'activity.add' | 'pr.update' }>

const byRank = (a: Issue, b: Issue) => a.rank - b.rank || a.number - b.number
const sameLink = (a: Link, b: Link) => a.blockerId === b.blockerId && a.blockedId === b.blockedId

/**
 * Client-side mirror of one project. Loads a snapshot, then applies the server's event stream,
 * replaying from the last seen sequence number after any disconnect. Writes are applied
 * optimistically and rolled back if the server refuses them.
 */
export class ProjectStore {
  private state: ProjectState = {
    phase: 'loading',
    deleted: false,
    connected: false,
    project: null,
    statuses: [],
    issues: [],
    links: [],
    members: [],
    presence: [],
  }
  private listeners = new Set<() => void>()
  private feedListeners = new Set<(e: FeedEvent) => void>()
  private ws: WebSocket | null = null
  private seq = 0
  private retry = 0
  private retryTimer: ReturnType<typeof setTimeout> | undefined
  private stopped = false
  private gen = 0
  private myPresence: { viewing: string | null; editing: string | null } = { viewing: null, editing: null }

  constructor(
    readonly projectId: string,
    private onError: (message: string) => void,
  ) {}

  subscribe = (l: () => void) => {
    this.listeners.add(l)
    return () => this.listeners.delete(l)
  }
  getState = () => this.state

  /** Comments and activity for the open issue panel. */
  onFeed(l: (e: FeedEvent) => void): () => void {
    this.feedListeners.add(l)
    return () => {
      this.feedListeners.delete(l)
    }
  }

  private set(patch: Partial<ProjectState>) {
    this.state = { ...this.state, ...patch }
    this.listeners.forEach((l) => l())
  }

  async start() {
    // A stop() while the snapshot is loading (e.g. React StrictMode's remount) must not leave a stray socket.
    const gen = ++this.gen
    this.stopped = false
    await this.load()
    if (gen === this.gen && !this.stopped) this.connect()
  }

  stop() {
    this.gen++
    this.stopped = true
    clearTimeout(this.retryTimer)
    this.ws?.close()
    this.ws = null
  }

  private async load() {
    try {
      const snap = await api.get<Snapshot>(`/projects/${this.projectId}`)
      this.seq = snap.seq
      this.set({
        phase: 'ready',
        error: undefined,
        project: snap.project,
        statuses: snap.statuses,
        issues: [...snap.issues].sort(byRank),
        links: snap.links,
        members: snap.members,
      })
    } catch (e) {
      this.set({ phase: 'error', error: e instanceof ApiError && e.status === 404 ? 'not-found' : errorMessage(e) })
      this.stopped = true
    }
  }

  private connect() {
    if (this.stopped) return
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(`${proto}://${location.host}/api/projects/${this.projectId}/live?since=${this.seq}`)
    this.ws = ws
    ws.onopen = () => {
      this.retry = 0
      this.set({ connected: true })
      this.sendPresence()
    }
    ws.onmessage = (m) => {
      const msg = JSON.parse(m.data)
      if (msg.type === 'event') {
        if (msg.seq <= this.seq) return
        this.seq = msg.seq
        this.apply(msg.event as ServerEvent)
      } else if (msg.type === 'presence') {
        this.set({ presence: msg.users })
      } else if (msg.type === 'resync') {
        void this.load()
      }
    }
    ws.onclose = () => {
      if (this.ws !== ws) return
      this.ws = null
      this.set({ connected: false })
      if (this.stopped) return
      const delay = Math.min(15000, 500 * 2 ** this.retry++) * (0.75 + Math.random() / 2)
      this.retryTimer = setTimeout(() => this.connect(), delay)
    }
  }

  private apply(e: ServerEvent) {
    const s = this.state
    switch (e.type) {
      case 'issue.upsert': {
        const cur = s.issues.find((i) => i.id === e.issue.id)
        if (cur && cur.version > e.issue.version) return
        const issues = cur ? s.issues.map((i) => (i.id === e.issue.id ? e.issue : i)) : [...s.issues, e.issue]
        this.set({ issues: issues.sort(byRank) })
        break
      }
      case 'issue.delete':
        this.set({
          issues: s.issues.filter((i) => i.id !== e.id),
          links: s.links.filter((l) => l.blockerId !== e.id && l.blockedId !== e.id),
        })
        break
      case 'link.add':
        if (!s.links.some((l) => sameLink(l, e.link))) this.set({ links: [...s.links, e.link] })
        break
      case 'link.remove':
        this.set({ links: s.links.filter((l) => !sameLink(l, e.link)) })
        break
      case 'statuses.update':
        this.set({ statuses: e.statuses })
        break
      case 'project.update':
        this.set({ project: e.project })
        break
      case 'project.delete':
        this.set({ deleted: true })
        this.stop()
        break
      case 'member.join':
        if (!s.members.some((m) => m.id === e.member.id)) this.set({ members: [...s.members, e.member].sort((a, b) => a.name.localeCompare(b.name)) })
        break
      default:
        this.feedListeners.forEach((l) => l(e))
    }
  }

  // ---- presence ----

  setPresence(p: Partial<typeof this.myPresence>) {
    this.myPresence = { ...this.myPresence, ...p }
    this.sendPresence()
  }

  private sendPresence() {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify({ type: 'presence', ...this.myPresence }))
  }

  // ---- writes ----

  private replaceIssue(issue: Issue) {
    this.set({ issues: this.state.issues.map((i) => (i.id === issue.id ? issue : i)).sort(byRank) })
  }

  private reconcile(issue: Issue) {
    const cur = this.state.issues.find((i) => i.id === issue.id)
    if (!cur || cur.version <= issue.version) this.replaceIssue(issue)
  }

  async createIssue(fields: Partial<Issue> & { title: string }, blockerIds: string[] = []): Promise<Issue | null> {
    try {
      const issue = await api.post<Issue>(`/projects/${this.projectId}/issues`, fields)
      if (!this.state.issues.some((i) => i.id === issue.id)) this.set({ issues: [...this.state.issues, issue].sort(byRank) })
      for (const b of blockerIds) await this.addBlocker(issue.id, b)
      return issue
    } catch (e) {
      this.onError(errorMessage(e))
      return null
    }
  }

  /**
   * Applies `fields` optimistically. Pass `base` (the value the user started editing from) for
   * free-text fields so a concurrent edit by someone else is detected instead of overwritten.
   */
  async updateIssue(id: string, fields: Partial<Issue>, base?: Partial<Pick<Issue, 'title' | 'description'>>): Promise<'ok' | 'conflict' | 'error'> {
    const prev = this.state.issues.find((i) => i.id === id)
    if (!prev) return 'error'
    this.replaceIssue({ ...prev, ...fields })
    try {
      this.reconcile(await api.patch<Issue>(`/issues/${id}`, base ? { ...fields, base } : fields))
      return 'ok'
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && e.data && typeof e.data === 'object' && 'issue' in e.data) {
        this.reconcile((e.data as { issue: Issue }).issue)
        return 'conflict'
      }
      const cur = this.state.issues.find((i) => i.id === id)
      if (cur) this.replaceIssue({ ...cur, ...pick(prev, Object.keys(fields) as (keyof Issue)[]) })
      this.onError(errorMessage(e))
      return 'error'
    }
  }

  async deleteIssue(id: string) {
    const prev = this.state
    this.set({ issues: prev.issues.filter((i) => i.id !== id), links: prev.links.filter((l) => l.blockerId !== id && l.blockedId !== id) })
    try {
      await api.del(`/issues/${id}`)
    } catch (e) {
      this.set({ issues: prev.issues, links: prev.links })
      this.onError(errorMessage(e))
    }
  }

  async addBlocker(issueId: string, blockerId: string) {
    const link = { blockerId, blockedId: issueId }
    if (this.state.links.some((l) => sameLink(l, link))) return
    this.set({ links: [...this.state.links, link] })
    try {
      await api.post(`/issues/${issueId}/blockers`, { blockerId })
    } catch (e) {
      this.set({ links: this.state.links.filter((l) => !sameLink(l, link)) })
      this.onError(errorMessage(e))
    }
  }

  async removeBlocker(issueId: string, blockerId: string) {
    const link = { blockerId, blockedId: issueId }
    this.set({ links: this.state.links.filter((l) => !sameLink(l, link)) })
    try {
      await api.del(`/issues/${issueId}/blockers/${blockerId}`)
    } catch (e) {
      this.set({ links: [...this.state.links, link] })
      this.onError(errorMessage(e))
    }
  }

  async updateProject(fields: Partial<Pick<Project, 'name' | 'description'>>) {
    try {
      this.set({ project: await api.patch<Project>(`/projects/${this.projectId}`, fields) })
    } catch (e) {
      this.onError(errorMessage(e))
    }
  }

  /** Status edits wait for the server, which renumbers the workflow and broadcasts the result. */
  async statusOp(fn: () => Promise<unknown>) {
    try {
      await fn()
      return true
    } catch (e) {
      this.onError(errorMessage(e))
      return false
    }
  }

  refreshMembers(members: Member[]) {
    this.set({ members })
  }
}

function pick<T extends object>(obj: T, keys: (keyof T)[]): Partial<T> {
  const out: Partial<T> = {}
  for (const k of keys) out[k] = obj[k]
  return out
}
