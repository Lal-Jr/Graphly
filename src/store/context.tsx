import { createContext, useContext, useEffect, useMemo, useState, useSyncExternalStore, type ReactNode } from 'react'
import { analyze, type Analysis } from '../lib/graph'
import type { Task } from '../lib/types'
import type { Board, Presence } from './doc'

interface BoardState {
  board: Board
  tasks: Task[]
  byId: Map<string, Task>
  analysis: Analysis
  presence: Presence[]
  /** False until the local IndexedDB copy has loaded. */
  loaded: boolean
}

const Ctx = createContext<BoardState | null>(null)

/** `board` lives for the whole page — y-webrtc allows only one provider per room. */
export function BoardProvider({ board, children }: { board: Board; children: ReactNode }) {
  const [loaded, setLoaded] = useState(false)
  useEffect(() => {
    board.idb.whenSynced.then(() => setLoaded(true))
  }, [board])

  const tasks = useSyncExternalStore(board.subscribe, board.getTasks)
  const presence = useSyncExternalStore(board.subscribePresence, board.getPresence)
  const byId = useMemo(() => new Map(tasks.map((t) => [t.id, t])), [tasks])
  const analysis = useMemo(() => analyze(tasks), [tasks])

  return <Ctx.Provider value={{ board, tasks, byId, analysis, presence, loaded }}>{children}</Ctx.Provider>
}

export function useBoard() {
  const v = useContext(Ctx)
  if (!v) throw new Error('useBoard must be used inside BoardProvider')
  return v
}

/** Other users currently viewing a given task. */
export function useViewers(taskId: string) {
  const { presence, board } = useBoard()
  return presence.filter((p) => p.viewing === taskId && p.clientId !== board.clientId)
}
