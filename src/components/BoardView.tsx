import { useMemo, useRef, useState } from 'react'
import { COLUMNS, type Status, type Task } from '../lib/types'
import { useBoard } from '../store/context'
import { TaskCard } from './TaskCard'
import { useToast } from './ui'

export function BoardView({ filter, onOpen }: { filter: (t: Task) => boolean; onOpen: (id: string) => void }) {
  const { board, tasks, byId, analysis } = useBoard()
  const toast = useToast()
  const dragging = useRef<string | null>(null)
  const [hover, setHover] = useState<string | null>(null)
  const [overCol, setOverCol] = useState<Status | null>(null)

  // Hovering a card lights up everything upstream and downstream of it.
  const related = useMemo(() => {
    const map = new Map<string, 'upstream' | 'downstream' | 'self'>()
    if (!hover || !byId.has(hover)) return map
    const walk = (start: string, next: (id: string) => string[], tag: 'upstream' | 'downstream') => {
      const stack = [...next(start)]
      while (stack.length) {
        const id = stack.pop()!
        if (map.has(id)) continue
        map.set(id, tag)
        stack.push(...next(id))
      }
    }
    map.set(hover, 'self')
    walk(hover, (id) => byId.get(id)?.dependsOn.filter((d) => byId.has(d)) ?? [], 'upstream')
    walk(hover, (id) => tasks.filter((t) => t.dependsOn.includes(id)).map((t) => t.id), 'downstream')
    return map
  }, [hover, byId, tasks])

  const move = (id: string, status: Status, beforeId?: string) => {
    const task = byId.get(id)
    if (!task) return
    const column = tasks.filter((t) => t.status === status && t.id !== id)
    let order: number
    const idx = beforeId ? column.findIndex((t) => t.id === beforeId) : -1
    if (idx === -1) order = (column.at(-1)?.order ?? 0) + 1000
    else order = idx === 0 ? column[0].order - 1000 : (column[idx - 1].order + column[idx].order) / 2
    board.updateTask(id, { status, order })

    const deps = analysis.blockedBy.get(id)
    if (deps && status !== 'backlog' && status !== 'todo' && task.status !== status) {
      const names = deps.map((d) => `“${byId.get(d)?.title}”`).join(', ')
      toast(`Heads up: “${task.title}” is still blocked by ${names}`, 'warn')
    }
    if (status === 'done' && task.status !== 'done') {
      const unblocked = (analysis.dependents.get(id) ?? []).filter((d) => analysis.blockedBy.get(d)?.length === 1)
      if (unblocked.length) toast(`Unblocked: ${unblocked.map((d) => `“${byId.get(d)?.title}”`).join(', ')}`)
    }
  }

  return (
    <div className="board">
      {COLUMNS.map((col) => {
        const items = tasks.filter((t) => t.status === col.id && filter(t))
        const days = items.reduce((s, t) => s + (t.estimate || 0), 0)
        return (
          <section
            key={col.id}
            className={`column${overCol === col.id ? ' over' : ''}`}
            onDragOver={(e) => {
              e.preventDefault()
              setOverCol(col.id)
            }}
            onDragLeave={(e) => {
              if (!e.currentTarget.contains(e.relatedTarget as Node)) setOverCol(null)
            }}
            onDrop={(e) => {
              e.preventDefault()
              setOverCol(null)
              if (dragging.current) move(dragging.current, col.id)
              dragging.current = null
            }}
          >
            <header>
              <h2>{col.label}</h2>
              <span className="count">{items.length}</span>
              <span className="col-days">{days}d</span>
            </header>
            <div className="cards">
              {items.map((t) => (
                <TaskCard
                  key={t.id}
                  task={t}
                  related={related.get(t.id) ?? null}
                  onOpen={onOpen}
                  onHover={setHover}
                  onDragStart={(id) => (dragging.current = id)}
                  onDropBefore={(beforeId) => {
                    setOverCol(null)
                    if (dragging.current && dragging.current !== beforeId) move(dragging.current, col.id, beforeId)
                    dragging.current = null
                  }}
                />
              ))}
            </div>
            <AddTask status={col.id} />
          </section>
        )
      })}
    </div>
  )
}

function AddTask({ status }: { status: Status }) {
  const { board } = useBoard()
  const [title, setTitle] = useState('')
  return (
    <form
      className="add-task"
      onSubmit={(e) => {
        e.preventDefault()
        if (!title.trim()) return
        board.addTask({ title: title.trim(), status })
        setTitle('')
      }}
    >
      <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="+ Add task" aria-label="New task title" />
    </form>
  )
}
