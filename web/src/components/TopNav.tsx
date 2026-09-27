import { useState } from 'react'
import { Link, NavLink, useNavigate } from 'react-router-dom'
import { api, errorMessage } from '../api'
import { useSession } from '../store/session'
import { useTheme, type ThemeMode } from '../store/theme'
import type { Workspace } from '../types'
import { Icon, Logo, type IconName } from '../ui/icons'
import { Avatar, Button, MenuList, Modal, Popover } from '../ui/primitives'
import { useToast } from '../ui/toast'

export function TopNav({ onCreate, createLabel = 'Create' }: { onCreate?: () => void; createLabel?: string }) {
  const { user, workspaces, workspace, setWorkspace, logout, refresh } = useSession()
  const { mode, setMode } = useTheme()
  const nav = useNavigate()
  const [newWs, setNewWs] = useState(false)

  return (
    <header className="topnav">
      <Link to="/" className="brand">
        <Logo size={26} />
        <span>Graphly</span>
      </Link>

      <Popover
        width={260}
        trigger={({ toggle, ref }) => (
          <button ref={ref} className="nav-btn" onClick={toggle}>
            <span className="truncate">{workspace?.name ?? 'Workspace'}</span>
            <Icon name="chevron-down" size={14} />
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
      <NavLink to="/" end className="nav-link">
        Your work
      </NavLink>

      {onCreate && (
        <Button variant="primary" size="sm" icon={<Icon name="plus" size={16} />} onClick={onCreate}>
          {createLabel}
        </Button>
      )}
      <span className="spacer" />

      <button
        className="nav-search"
        onClick={() => window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true }))}
        title="Search (⌘K)"
      >
        <Icon name="search" size={16} />
        <span>Search</span>
        <kbd>⌘K</kbd>
      </button>

      <Popover
        align="end"
        width={200}
        trigger={({ toggle, ref }) => (
          <button ref={ref} className="icon-btn nav-icon" onClick={toggle} title="Theme">
            <Icon name={mode === 'light' ? 'sun' : mode === 'dark' ? 'moon' : 'monitor'} size={18} />
          </button>
        )}
      >
        {(close) => (
          <MenuList
            items={(['light', 'dark', 'system'] as ThemeMode[]).map((m) => ({
              value: m,
              label: m === 'system' ? 'Match system' : m === 'light' ? 'Light' : 'Dark',
              icon: <Icon name={(m === 'light' ? 'sun' : m === 'dark' ? 'moon' : 'monitor') as IconName} size={14} />,
            }))}
            selected={mode}
            onSelect={(m) => {
              setMode(m)
              close()
            }}
          />
        )}
      </Popover>

      <Popover
        align="end"
        width={260}
        trigger={({ toggle, ref }) => (
          <button ref={ref} className="avatar-btn" onClick={toggle} title={user?.name}>
            <Avatar user={user} size={30} />
          </button>
        )}
      >
        {(close) => (
          <div>
            <div className="menu-profile">
              <Avatar user={user} size={36} />
              <div>
                <b>{user?.name}</b>
                <span className="muted small">{user?.email}</span>
              </div>
            </div>
            <MenuList
              items={[
                { value: 'profile', label: 'Profile', icon: <Icon name="settings" size={14} /> },
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
          </div>
        )}
      </Popover>
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
    </header>
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
