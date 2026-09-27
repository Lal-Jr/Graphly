import { useState } from 'react'
import { api, errorMessage } from '../api'
import { TopNav } from '../components/TopNav'
import { useSession } from '../store/session'
import { useTheme, type ThemeMode } from '../store/theme'
import type { User } from '../types'
import { Icon } from '../ui/icons'
import { Avatar, Button } from '../ui/primitives'
import { useToast } from '../ui/toast'

const COLORS = ['#0C66E4', '#1F845A', '#E56910', '#AE2E24', '#6E5DC6', '#206A83', '#943D73', '#5B7F24']

export function ProfilePage() {
  const { user, setUser } = useSession()
  const { mode, setMode } = useTheme()
  const toast = useToast()
  const [name, setName] = useState(user?.name ?? '')
  const save = (patch: Partial<User>) =>
    api.patch<User>('/me', patch).then(
      (u) => {
        setUser(u)
        toast('Profile updated', 'success')
      },
      (e) => toast(errorMessage(e), 'error'),
    )
  if (!user) return null
  return (
    <div className="app">
      <TopNav />
      <main className="home">
        <div className="home-inner narrow">
          <h1>Profile</h1>
          <section className="settings-card">
            <div className="profile-head">
              <Avatar user={{ ...user, name }} size={64} />
              <div>
                <b>{user.name}</b>
                <span className="muted">{user.email}</span>
              </div>
            </div>
            <form
              className="settings-form"
              onSubmit={(e) => {
                e.preventDefault()
                void save({ name })
              }}
            >
              <label className="stack-field">
                Full name
                <input className="field-input" value={name} onChange={(e) => setName(e.target.value)} maxLength={80} required />
              </label>
              <div className="stack-field">
                Avatar colour
                <div className="swatches">
                  {COLORS.map((c) => (
                    <button
                      key={c}
                      type="button"
                      className={`swatch-btn${user.color === c ? ' on' : ''}`}
                      style={{ background: c }}
                      onClick={() => save({ color: c })}
                      aria-label={`Colour ${c}`}
                    />
                  ))}
                </div>
              </div>
              <div>
                <Button type="submit" variant="primary" disabled={name === user.name}>
                  Save
                </Button>
              </div>
            </form>
          </section>
          <section className="settings-card">
            <h2>Appearance</h2>
            <div className="theme-choices">
              {(['light', 'dark', 'system'] as ThemeMode[]).map((m) => (
                <button key={m} className={`theme-choice${mode === m ? ' on' : ''}`} onClick={() => setMode(m)}>
                  <span className={`theme-preview theme-preview-${m}`} />
                  <span>
                    <Icon name={m === 'light' ? 'sun' : m === 'dark' ? 'moon' : 'monitor'} size={14} /> {m === 'system' ? 'Match system' : m[0].toUpperCase() + m.slice(1)}
                  </span>
                </button>
              ))}
            </div>
          </section>
        </div>
      </main>
    </div>
  )
}
