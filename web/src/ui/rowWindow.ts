import { useEffect, useState, type RefObject } from 'react'

/** Lists longer than this render only the rows near the viewport. */
const WINDOW_FROM = 300
const OVERSCAN = 20

/**
 * Windowing for long lists: the slice of rows to render for a scroll container, and the row height
 * (measured from the first row matching `rowSelector`) to size the spacers that stand in for the rest.
 */
export function useRowWindow(count: number, scroller: RefObject<HTMLElement | null>, rowSelector: string) {
  const [range, setRange] = useState({ start: 0, end: 80, rowH: 47 })
  useEffect(() => {
    const el = scroller.current
    if (!el || count <= WINDOW_FROM) return
    const update = () => {
      const row = el.querySelector<HTMLElement>(rowSelector)
      const rowH = row?.getBoundingClientRect().height || 47
      const start = Math.max(0, Math.floor(el.scrollTop / rowH) - OVERSCAN)
      const end = Math.min(count, start + Math.ceil(el.clientHeight / rowH) + 2 * OVERSCAN)
      setRange((r) => (r.start === start && r.end === end && r.rowH === rowH ? r : { start, end, rowH }))
    }
    update()
    el.addEventListener('scroll', update, { passive: true })
    window.addEventListener('resize', update)
    return () => {
      el.removeEventListener('scroll', update)
      window.removeEventListener('resize', update)
    }
  }, [count, scroller, rowSelector])
  return count <= WINDOW_FROM ? { start: 0, end: count, rowH: 0 } : { ...range, end: Math.min(range.end, count) }
}

