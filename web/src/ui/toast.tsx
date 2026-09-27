import { createContext, useCallback, useContext, useState, type ReactNode } from 'react'
import { Icon } from './icons'

type Tone = 'info' | 'success' | 'warning' | 'error'
interface Toast {
  id: number
  text: string
  tone: Tone
}

const Ctx = createContext<(text: string, tone?: Tone) => void>(() => {})
let nextId = 0

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const dismiss = (id: number) => setToasts((t) => t.filter((x) => x.id !== id))
  const push = useCallback((text: string, tone: Tone = 'info') => {
    const id = ++nextId
    setToasts((t) => [...t.slice(-3), { id, text, tone }])
    setTimeout(() => dismiss(id), tone === 'error' ? 6000 : 4000)
  }, [])
  return (
    <Ctx.Provider value={push}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className={`toast toast-${t.tone}`}>
            <Icon name={t.tone === 'success' ? 'check-circle' : t.tone === 'info' ? 'info' : 'warning'} size={16} />
            <span>{t.text}</span>
            <button className="icon-btn" onClick={() => dismiss(t.id)} aria-label="Dismiss">
              <Icon name="x" size={14} />
            </button>
          </div>
        ))}
      </div>
    </Ctx.Provider>
  )
}

export const useToast = () => useContext(Ctx)
