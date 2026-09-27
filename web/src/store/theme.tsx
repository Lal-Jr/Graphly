import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

export type ThemeMode = 'light' | 'dark' | 'system'
const KEY = 'graphly:theme'

const Ctx = createContext<{ mode: ThemeMode; resolved: 'light' | 'dark'; setMode: (m: ThemeMode) => void }>({
  mode: 'system',
  resolved: 'light',
  setMode: () => {},
})

const media = () => window.matchMedia('(prefers-color-scheme: dark)')

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [mode, setModeState] = useState<ThemeMode>(() => {
    try {
      return (localStorage.getItem(KEY) as ThemeMode) || 'system'
    } catch {
      return 'system'
    }
  })
  const [systemDark, setSystemDark] = useState(() => media().matches)

  useEffect(() => {
    const m = media()
    const on = () => setSystemDark(m.matches)
    m.addEventListener('change', on)
    return () => m.removeEventListener('change', on)
  }, [])

  const resolved = mode === 'system' ? (systemDark ? 'dark' : 'light') : mode
  useEffect(() => {
    document.documentElement.dataset.theme = resolved
    // `only light` stops browsers with forced dark mode from repainting a theme the user chose.
    document.documentElement.style.colorScheme = resolved === 'light' ? 'only light' : 'dark'
  }, [resolved])

  const setMode = (m: ThemeMode) => {
    setModeState(m)
    try {
      localStorage.setItem(KEY, m)
    } catch {}
  }
  return <Ctx.Provider value={{ mode, resolved, setMode }}>{children}</Ctx.Provider>
}

export const useTheme = () => useContext(Ctx)
