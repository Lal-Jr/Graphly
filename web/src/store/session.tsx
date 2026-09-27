import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import { api, ApiError } from '../api'
import type { User, Workspace } from '../types'

interface Session {
  user: User | null
  workspaces: Workspace[]
  workspace: Workspace | null
  loading: boolean
  setWorkspace: (id: string) => void
  refresh: () => Promise<void>
  setUser: (u: User) => void
  logout: () => Promise<void>
}

const Ctx = createContext<Session | null>(null)
const WS_KEY = 'graphly:workspace'

function storedWorkspace() {
  try {
    return localStorage.getItem(WS_KEY)
  } catch {
    return null
  }
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [workspaces, setWorkspaces] = useState<Workspace[]>([])
  const [wsId, setWsId] = useState<string | null>(storedWorkspace)
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    try {
      const me = await api.get<{ user: User; workspaces: Workspace[] }>('/me')
      setUser(me.user)
      setWorkspaces(me.workspaces)
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) console.error(e)
      setUser(null)
      setWorkspaces([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const setWorkspace = useCallback((id: string) => {
    setWsId(id)
    try {
      localStorage.setItem(WS_KEY, id)
    } catch {}
  }, [])

  const logout = useCallback(async () => {
    await api.post('/auth/logout')
    setUser(null)
    setWorkspaces([])
  }, [])

  const workspace = workspaces.find((w) => w.id === wsId) ?? workspaces[0] ?? null
  return (
    <Ctx.Provider value={{ user, workspaces, workspace, loading, setWorkspace, refresh, setUser, logout }}>{children}</Ctx.Provider>
  )
}

export function useSession() {
  const v = useContext(Ctx)
  if (!v) throw new Error('useSession must be used inside SessionProvider')
  return v
}
