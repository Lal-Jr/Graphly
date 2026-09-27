import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type ReactNode,
  type RefObject,
} from 'react'
import { createPortal } from 'react-dom'
import type { Category } from '../types'
import { Icon } from './icons'

export function initials(name: string) {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .map((w) => w[0])
    .join('')
    .slice(0, 2)
    .toUpperCase()
}

export function Avatar({
  user,
  size = 24,
  ring,
  title,
}: {
  user: { name: string; color: string } | null | undefined
  size?: number
  ring?: boolean
  title?: string
}) {
  if (!user)
    return (
      <span className="avatar avatar-empty" style={{ width: size, height: size }} title={title ?? 'Unassigned'}>
        <Icon name="users" size={size * 0.55} />
      </span>
    )
  return (
    <span
      className={`avatar${ring ? ' avatar-ring' : ''}`}
      title={title ?? user.name}
      style={{ background: user.color, width: size, height: size, fontSize: Math.round(size * 0.4) }}
    >
      {initials(user.name)}
    </span>
  )
}

export function AvatarStack({ users, size = 24, max = 4 }: { users: { name: string; color: string; key: string }[]; size?: number; max?: number }) {
  return (
    <span className="avatar-stack">
      {users.slice(0, max).map((u) => (
        <Avatar key={u.key} user={u} size={size} ring />
      ))}
      {users.length > max && (
        <span className="avatar avatar-more" style={{ width: size, height: size, fontSize: size * 0.38 }}>
          +{users.length - max}
        </span>
      )}
    </span>
  )
}

type Variant = 'primary' | 'default' | 'subtle' | 'danger' | 'warning'

export function Button({
  variant = 'default',
  size = 'md',
  icon,
  children,
  className = '',
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: 'sm' | 'md'; icon?: ReactNode }) {
  return (
    <button {...rest} className={`btn btn-${variant} btn-${size} ${className}`}>
      {icon}
      {children && <span>{children}</span>}
    </button>
  )
}

export function Lozenge({ category, children, bold }: { category: Category | 'danger' | 'warning' | 'discovery'; children: ReactNode; bold?: boolean }) {
  return <span className={`lozenge lozenge-${category}${bold ? ' bold' : ''}`}>{children}</span>
}

export function Spinner({ size = 20 }: { size?: number }) {
  return <span className="spinner" style={{ width: size, height: size }} aria-label="Loading" />
}

export function useClickOutside(refs: RefObject<HTMLElement | null>[], onOutside: () => void, active = true) {
  useEffect(() => {
    if (!active) return
    const onDown = (e: MouseEvent) => {
      if (refs.every((r) => !r.current?.contains(e.target as Node))) onOutside()
    }
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onOutside()
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [refs, onOutside, active])
}

/**
 * A floating panel anchored to a trigger. Rendered in a portal with fixed positioning so it
 * escapes scroll containers (board columns, modals), flipping above the trigger near the bottom.
 */
export function Popover({
  trigger,
  children,
  align = 'start',
  width,
  className = '',
}: {
  trigger: (props: { open: boolean; toggle: () => void; ref: RefObject<HTMLButtonElement | null> }) => ReactNode
  children: (close: () => void) => ReactNode
  align?: 'start' | 'end'
  width?: number
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const panelRef = useRef<HTMLDivElement | null>(null)
  const [pos, setPos] = useState<{ top: number; left: number; maxHeight: number } | null>(null)
  const close = useCallback(() => setOpen(false), [])
  const refs = useRef([triggerRef, panelRef]).current
  useClickOutside(refs, close, open)

  useLayoutEffect(() => {
    if (!open || !triggerRef.current) return
    const place = () => {
      const r = triggerRef.current!.getBoundingClientRect()
      const panel = panelRef.current
      const pw = panel?.offsetWidth ?? width ?? 240
      const ph = panel?.offsetHeight ?? 300
      const below = window.innerHeight - r.bottom - 8
      const above = r.top - 8
      const flip = below < Math.min(ph, 240) && above > below
      let left = align === 'end' ? r.right - pw : r.left
      left = Math.max(8, Math.min(left, window.innerWidth - pw - 8))
      setPos({
        top: flip ? Math.max(8, r.top - 4 - Math.min(ph, above)) : r.bottom + 4,
        left,
        maxHeight: Math.max(160, flip ? above : below),
      })
    }
    place()
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    return () => {
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
    }
  }, [open, align, width])

  return (
    <>
      {trigger({ open, toggle: () => setOpen((o) => !o), ref: triggerRef })}
      {open &&
        createPortal(
          <div
            ref={panelRef}
            className={`popover ${className}`}
            style={{ top: pos?.top ?? -9999, left: pos?.left ?? -9999, width, maxHeight: pos?.maxHeight }}
            onMouseDown={(e) => e.stopPropagation()}
          >
            {children(close)}
          </div>,
          document.body,
        )}
    </>
  )
}

export interface MenuItem<T extends string> {
  value: T
  label: ReactNode
  icon?: ReactNode
  hint?: ReactNode
  danger?: boolean
}

/** A dropdown list of choices with type-to-filter and keyboard navigation. */
export function MenuList<T extends string>({
  items,
  selected,
  onSelect,
  searchable,
  placeholder = 'Search…',
  empty = 'No matches',
  filterText,
}: {
  items: MenuItem<T>[]
  selected?: T | T[] | null
  onSelect: (v: T) => void
  searchable?: boolean
  placeholder?: string
  empty?: string
  filterText?: (item: MenuItem<T>) => string
}) {
  const [q, setQ] = useState('')
  const [active, setActive] = useState(0)
  const list = useRef<HTMLDivElement>(null)
  const shown = q
    ? items.filter((i) => (filterText?.(i) ?? String(i.label)).toLowerCase().includes(q.toLowerCase()))
    : items
  const isSel = (v: T) => (Array.isArray(selected) ? selected.includes(v) : selected === v)

  useEffect(() => setActive(0), [q])
  useEffect(() => {
    list.current?.querySelector<HTMLElement>(`[data-idx="${active}"]`)?.scrollIntoView({ block: 'nearest' })
  }, [active])

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActive((a) => Math.min(a + 1, shown.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive((a) => Math.max(a - 1, 0))
    } else if (e.key === 'Enter' && shown[active]) {
      e.preventDefault()
      onSelect(shown[active].value)
    }
  }

  return (
    <div className="menu" onKeyDown={onKey}>
      {searchable ? (
        <input className="menu-search" autoFocus placeholder={placeholder} value={q} onChange={(e) => setQ(e.target.value)} />
      ) : (
        <span tabIndex={0} autoFocus className="sr-focus" ref={(el) => el?.focus()} />
      )}
      <div className="menu-items" ref={list} role="listbox">
        {shown.map((item, idx) => (
          <button
            key={item.value}
            data-idx={idx}
            role="option"
            aria-selected={isSel(item.value)}
            className={`menu-item${idx === active ? ' active' : ''}${isSel(item.value) ? ' selected' : ''}${item.danger ? ' danger' : ''}`}
            onMouseEnter={() => setActive(idx)}
            onClick={() => onSelect(item.value)}
          >
            {item.icon}
            <span className="menu-label">{item.label}</span>
            {item.hint && <span className="menu-hint">{item.hint}</span>}
            {isSel(item.value) && <Icon name="check" size={14} className="menu-check" />}
          </button>
        ))}
        {shown.length === 0 && <div className="menu-empty">{empty}</div>}
      </div>
    </div>
  )
}

const modalStack: object[] = []

export function Modal({
  onClose,
  children,
  width = 560,
  label,
  className = '',
}: {
  onClose: () => void
  children: ReactNode
  width?: number
  label: string
  className?: string
}) {
  const panel = useRef<HTMLDivElement>(null)
  const closeRef = useRef(onClose)
  closeRef.current = onClose
  useEffect(() => {
    const token = {}
    modalStack.push(token)
    const onKey = (e: KeyboardEvent) => {
      // Only the topmost modal responds, and open popovers consume Escape first.
      if (e.key === 'Escape' && modalStack.at(-1) === token && !document.querySelector('.popover')) closeRef.current()
    }
    document.addEventListener('keydown', onKey)
    const prev = document.activeElement as HTMLElement | null
    return () => {
      document.removeEventListener('keydown', onKey)
      modalStack.splice(modalStack.indexOf(token), 1)
      prev?.focus?.()
    }
  }, [])
  return createPortal(
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div ref={panel} className={`modal ${className}`} style={{ maxWidth: width }} role="dialog" aria-modal="true" aria-label={label}>
        {children}
      </div>
    </div>,
    document.body,
  )
}

export function EmptyState({ icon, title, children }: { icon?: ReactNode; title: string; children?: ReactNode }) {
  return (
    <div className="empty-state">
      {icon}
      <h3>{title}</h3>
      {children}
    </div>
  )
}

/** Click-to-edit text. Commits on Enter/blur, reverts on Escape. */
export function InlineEdit({
  value,
  onCommit,
  className = '',
  placeholder,
  multiline,
  onEditingChange,
}: {
  value: string
  onCommit: (v: string) => void
  className?: string
  placeholder?: string
  multiline?: boolean
  onEditingChange?: (editing: boolean) => void
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(value)
  const setMode = (on: boolean) => {
    setEditing(on)
    onEditingChange?.(on)
  }
  if (!editing)
    return (
      <div
        className={`inline-edit ${className}`}
        tabIndex={0}
        role="button"
        onClick={() => {
          setDraft(value)
          setMode(true)
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            setDraft(value)
            setMode(true)
          }
        }}
      >
        {value || <span className="placeholder">{placeholder}</span>}
      </div>
    )
  const commit = () => {
    setMode(false)
    if (draft.trim() && draft !== value) onCommit(draft.trim())
  }
  const props = {
    className: `inline-edit-input ${className}`,
    autoFocus: true,
    value: draft,
    onChange: (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => setDraft(e.target.value),
    onBlur: commit,
    onKeyDown: (e: React.KeyboardEvent) => {
      if (e.key === 'Enter' && (!multiline || !e.shiftKey)) {
        e.preventDefault()
        commit()
      } else if (e.key === 'Escape') {
        e.stopPropagation()
        setMode(false)
      }
    },
  }
  return multiline ? <textarea rows={2} {...props} /> : <input {...props} />
}

export function ConfirmDialog({
  title,
  body,
  confirm,
  onConfirm,
  onCancel,
}: {
  title: string
  body: ReactNode
  confirm: string
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <Modal onClose={onCancel} label={title} width={440}>
      <div className="modal-body">
        <h2 className="confirm-title">
          <Icon name="warning" size={20} className="text-danger" /> {title}
        </h2>
        <p>{body}</p>
        <div className="modal-actions">
          <Button variant="subtle" onClick={onCancel}>
            Cancel
          </Button>
          <Button variant="danger" autoFocus onClick={onConfirm}>
            {confirm}
          </Button>
        </div>
      </div>
    </Modal>
  )
}
