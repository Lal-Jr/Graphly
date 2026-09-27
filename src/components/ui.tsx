import { createContext, useCallback, useContext, useState, type ReactNode } from 'react'
import type { Presence } from '../store/doc'

export function Avatar({ user, size = 22, ring }: { user: Pick<Presence, 'name' | 'color'>; size?: number; ring?: boolean }) {
  const initials = user.name
    .split(/\s+/)
    .map((w) => w[0])
    .join('')
    .slice(0, 2)
    .toUpperCase()
  return (
    <span
      className={`avatar${ring ? ' ring' : ''}`}
      title={user.name}
      style={{ background: user.color, width: size, height: size, fontSize: size * 0.42 }}
    >
      {initials}
    </span>
  )
}

/** Deterministic color for assignee names that aren't online users. */
export function nameColor(name: string) {
  let h = 0
  for (const c of name) h = (h * 31 + c.charCodeAt(0)) % 360
  return `hsl(${h} 55% 48%)`
}

interface Toast {
  id: number
  text: string
  tone: 'info' | 'warn'
}
const ToastCtx = createContext<(text: string, tone?: Toast['tone']) => void>(() => {})

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const push = useCallback((text: string, tone: Toast['tone'] = 'info') => {
    const id = Math.random()
    setToasts((t) => [...t, { id, text, tone }])
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 3800)
  }, [])
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="toasts" role="status">
        {toasts.map((t) => (
          <div key={t.id} className={`toast ${t.tone}`}>
            {t.text}
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  )
}

export const useToast = () => useContext(ToastCtx)
