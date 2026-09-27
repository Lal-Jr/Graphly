import * as Y from 'yjs'
import { WebrtcProvider } from 'y-webrtc'
import { IndexeddbPersistence } from 'y-indexeddb'
import { nanoid } from 'nanoid'
import type { Priority, Status, Task } from '../lib/types'

export interface Presence {
  clientId: number
  name: string
  color: string
  /** Task the user currently has open. */
  viewing?: string
}

const COLORS = ['#6366f1', '#22c55e', '#f59e0b', '#ec4899', '#06b6d4', '#a855f7', '#ef4444', '#14b8a6']
const ANIMALS = ['Otter', 'Falcon', 'Lynx', 'Panda', 'Koala', 'Heron', 'Bison', 'Gecko', 'Orca', 'Raven']

function storedName(): string {
  try {
    const existing = localStorage.getItem('graphly:name')
    if (existing) return existing
    const name = `${ANIMALS[Math.floor(Math.random() * ANIMALS.length)]} ${Math.floor(Math.random() * 90 + 10)}`
    localStorage.setItem('graphly:name', name)
    return name
  } catch {
    return 'Guest'
  }
}

type TaskMap = Y.Map<unknown>

/**
 * A board is one Yjs document: `tasks` maps id → Y.Map of fields, and each task's
 * dependencies live in a nested Y.Map used as a set so concurrent edits merge cleanly.
 */
export class Board {
  readonly doc = new Y.Doc()
  readonly tasks = this.doc.getMap<TaskMap>('tasks')
  readonly undo = new Y.UndoManager(this.tasks, { captureTimeout: 400 })
  readonly rtc: WebrtcProvider
  readonly idb: IndexeddbPersistence
  readonly me: { name: string; color: string }

  private snapshot: Task[] = []
  private peers: Presence[] = []
  private listeners = new Set<() => void>()
  private presenceListeners = new Set<() => void>()

  constructor(readonly room: string) {
    const signaling = import.meta.env.VITE_SIGNALING?.split(',').filter(Boolean)
    this.idb = new IndexeddbPersistence(`graphly:${room}`, this.doc)
    this.rtc = new WebrtcProvider(`graphly:${room}`, this.doc, signaling?.length ? { signaling } : {})

    this.me = { name: storedName(), color: COLORS[this.doc.clientID % COLORS.length] }
    this.rtc.awareness.setLocalStateField('user', this.me)

    this.tasks.observeDeep(() => {
      this.snapshot = this.readTasks()
      this.listeners.forEach((l) => l())
    })
    this.rtc.awareness.on('change', () => {
      this.peers = this.readPresence()
      this.presenceListeners.forEach((l) => l())
    })
    this.peers = this.readPresence()
  }

  destroy() {
    this.rtc.destroy()
    this.idb.destroy()
    this.doc.destroy()
  }

  // ---- subscriptions (useSyncExternalStore) ----

  subscribe = (l: () => void) => {
    this.listeners.add(l)
    return () => this.listeners.delete(l)
  }
  getTasks = () => this.snapshot

  subscribePresence = (l: () => void) => {
    this.presenceListeners.add(l)
    return () => this.presenceListeners.delete(l)
  }
  getPresence = () => this.peers

  private readTasks(): Task[] {
    const out: Task[] = []
    this.tasks.forEach((m, id) => {
      const deps = m.get('dependsOn') as Y.Map<true> | undefined
      out.push({
        id,
        title: (m.get('title') as string) ?? '',
        description: (m.get('description') as string) ?? '',
        status: (m.get('status') as Status) ?? 'backlog',
        priority: (m.get('priority') as Priority) ?? 'medium',
        assignee: (m.get('assignee') as string) ?? '',
        estimate: (m.get('estimate') as number) ?? 1,
        dependsOn: deps ? [...deps.keys()] : [],
        order: (m.get('order') as number) ?? 0,
        createdAt: (m.get('createdAt') as number) ?? 0,
      })
    })
    return out.sort((a, b) => a.order - b.order)
  }

  private readPresence(): Presence[] {
    const out: Presence[] = []
    this.rtc.awareness.getStates().forEach((state, clientId) => {
      if (state.user) out.push({ clientId, ...state.user, viewing: state.viewing })
    })
    return out.sort((a, b) => a.clientId - b.clientId)
  }

  // ---- presence ----

  setName(name: string) {
    this.me.name = name.trim() || this.me.name
    try {
      localStorage.setItem('graphly:name', this.me.name)
    } catch {}
    this.rtc.awareness.setLocalStateField('user', { ...this.me })
  }

  setViewing(taskId: string | undefined) {
    this.rtc.awareness.setLocalStateField('viewing', taskId)
  }

  get clientId() {
    return this.doc.clientID
  }

  // ---- mutations ----

  addTask(fields: Partial<Omit<Task, 'id'>> & { title: string }): string {
    const id = nanoid(8)
    this.doc.transact(() => {
      const m = new Y.Map<unknown>()
      const deps = new Y.Map<true>()
      for (const d of fields.dependsOn ?? []) deps.set(d, true)
      m.set('title', fields.title)
      m.set('description', fields.description ?? '')
      m.set('status', fields.status ?? 'backlog')
      m.set('priority', fields.priority ?? 'medium')
      m.set('assignee', fields.assignee ?? '')
      m.set('estimate', fields.estimate ?? 1)
      m.set('order', fields.order ?? Date.now())
      m.set('createdAt', Date.now())
      m.set('dependsOn', deps)
      this.tasks.set(id, m)
    })
    return id
  }

  updateTask(id: string, fields: Partial<Omit<Task, 'id' | 'dependsOn'>>) {
    const m = this.tasks.get(id)
    if (!m) return
    this.doc.transact(() => {
      for (const [k, v] of Object.entries(fields)) if (m.get(k) !== v) m.set(k, v)
    })
  }

  addDependency(id: string, depId: string) {
    this.deps(id)?.set(depId, true)
  }

  removeDependency(id: string, depId: string) {
    this.deps(id)?.delete(depId)
  }

  deleteTask(id: string) {
    this.doc.transact(() => {
      this.tasks.delete(id)
      this.tasks.forEach((m) => (m.get('dependsOn') as Y.Map<true> | undefined)?.delete(id))
    })
  }

  clear() {
    this.doc.transact(() => [...this.tasks.keys()].forEach((k) => this.tasks.delete(k)))
  }

  private deps(id: string) {
    const m = this.tasks.get(id)
    if (!m) return undefined
    let deps = m.get('dependsOn') as Y.Map<true> | undefined
    if (!deps) {
      deps = new Y.Map<true>()
      m.set('dependsOn', deps)
    }
    return deps
  }
}
