import { useState, type ReactNode } from 'react'
import { Link, NavLink, useNavigate } from 'react-router-dom'
import { api, errorMessage } from '../api'
import { useSession } from '../store/session'
import { useTheme, type ThemeMode } from '../store/theme'
import type { Workspace } from '../types'
import { Icon, Logo, type IconName } from '../ui/icons'
import { Avatar, Button, MenuList, Modal, Popover } from '../ui/primitives'
import { useToast } from '../ui/toast'

const THEME_ICON: Record<ThemeMode, IconName> = { light: 'sun', dark: 'moon', system: 'monitor' }
const NEXT_THEME: Record<ThemeMode, ThemeMode> = { light: 'dark', dark: 'system', system: 'light' }

/**
 * The app's one piece of chrome: a floating rail with the workspace, search, create and navigation.
 * `children` adds a context section (a project's views), `footer` pins something above the user menu.
 */
export function Sidebar({
  onCreate,
  createLabel = 'New issue',
  children,
  footer,
}: {
  onCreate?: () => void
  createLabel?: string
  children?: ReactNode
  footer?: ReactNode
}) {
  const { user, workspaces, workspace, setWorkspace, logout, refresh } = useSession()
  const { mode, setMode } = useTheme()
  const nav = useNavigate()
  const [newWs, setNewWs] = useState(false)

  return (
    <aside className="sidebar">
      <div className="sb-brand">
        <Link to="/" className="brand" title="Home">
          <Logo size={26} />
          <span>Graphly</span>
        </Link>
        <span className="spacer" />
        <button className="icon-btn sm" onClick={() => setMode(NEXT_THEME[mode])} title={`Theme: ${mode} (click to change)`}>
          <Icon name={THEME_ICON[mode]} size={16} />
        </button>
      </div>

      <Popover
        width={248}
        trigger={({ toggle, ref, open }) => (
          <button ref={ref} className={`ws-switch${open ? ' open' : ''}`} onClick={toggle}>
            <span className="ws-mark">{workspace?.name.slice(0, 1).toUpperCase() ?? '·'}</span>
            <span className="ws-name">
              <b className="truncate">{workspace?.name ?? 'Workspace'}</b>
              <span className="muted small">{workspace?.role === 'admin' ? 'Admin' : 'Member'}</span>
            </span>
            <Icon name="chevrons" size={14} />
          </button>
        )}
      >
        {(close) => (
          <MenuList
            items={[
              ...workspaces.map((w) => ({ value: w.id, label: w.name, hint: w.role === 'admin' ? 'Admin' : undefined })),
              { value: '__new', label: 'Create workspace', icon: <Icon name="plus" size={14} /> },
            ]}
            selected={workspace?.id}
            onSelect={(v) => {
              close()
              if (v === '__new') setNewWs(true)
              else {
                setWorkspace(v)
                nav('/')
              }
            }}
          />
        )}
      </Popover>

      <div className="sb-actions">
        <button
          className="sb-search"
          onClick={() => window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true }))}
          title="Search and jump (⌘K)"
        >
          <Icon name="search" size={15} />
          <span>Search</span>
          <kbd>⌘K</kbd>
        </button>
        {onCreate && (
          <button className="sb-create" onClick={onCreate} title={`${createLabel} (C)`}>
            <Icon name="plus" size={16} />
          </button>
        )}
      </div>

      <nav className="sidebar-nav">
        <NavLink to="/" end className="sidebar-link">
          <Icon name="home" size={17} />
          Your work
        </NavLink>
        {children}
      </nav>

      <div className="sb-bottom">
        {footer}
        <Popover
          align="start"
          width={248}
          trigger={({ toggle, ref }) => (
            <button ref={ref} className="sb-user" onClick={toggle}>
              <Avatar user={user} size={28} />
              <span className="ws-name">
                <b className="truncate">{user?.name}</b>
                <span className="muted small truncate">{user?.email}</span>
              </span>
              <Icon name="more" size={16} />
            </button>
          )}
        >
          {(close) => (
            <MenuList
              items={[
                { value: 'profile', label: 'Profile & theme', icon: <Icon name="settings" size={14} /> },
                { value: 'logout', label: 'Log out', icon: <Icon name="logout" size={14} /> },
              ]}
              onSelect={async (v) => {
                close()
                if (v === 'logout') {
                  await logout()
                  nav('/login')
                } else nav('/profile')
              }}
            />
          )}
        </Popover>
      </div>

      {newWs && (
        <NewWorkspaceDialog
          onClose={() => setNewWs(false)}
          onCreated={async (w) => {
            await refresh()
            setWorkspace(w.id)
            setNewWs(false)
            nav('/')
          }}
        />
      )}
    </aside>
  )
}

function NewWorkspaceDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (w: Workspace) => void }) {
  const [name, setName] = useState('')
  const toast = useToast()
  return (
    <Modal onClose={onClose} label="Create workspace" width={440}>
      <form
        className="modal-body"
        onSubmit={async (e) => {
          e.preventDefault()
          try {
            onCreated(await api.post<Workspace>('/workspaces', { name }))
          } catch (err) {
            toast(errorMessage(err), 'error')
          }
        }}
      >
        <h2>Create workspace</h2>
        <p className="muted">Workspaces hold projects and the people who can see them.</p>
        <label className="stack-field">
          Name
          <input className="field-input" autoFocus required value={name} onChange={(e) => setName(e.target.value)} placeholder="Acme Engineering" />
        </label>
        <div className="modal-actions">
          <Button type="button" variant="subtle" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!name.trim()}>
            Create
          </Button>
        </div>
      </form>
    </Modal>
  )
}
