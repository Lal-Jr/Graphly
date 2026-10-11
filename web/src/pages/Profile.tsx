import { useEffect, useState } from 'react'
import { api, errorMessage } from '../api'
import { Sidebar } from '../components/Sidebar'
import { useSession } from '../store/session'
import { useTheme, type ThemeMode } from '../store/theme'
import type { ChannelPrefs, NotificationKind, NotificationSettings, User } from '../types'
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
      <Sidebar />
      <main className="app-main home">
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
          <NotificationSettingsCard />
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

const KIND_LABEL: Record<NotificationKind, [string, string]> = {
  unblocked: ['You were unblocked', 'The last open blocker on an issue you own is done.'],
  mention: ['You were mentioned', 'Someone @mentioned you in a comment or description.'],
  late: ['Your work is forecast late', 'An issue you own now lands after its due date.'],
  date_moved: ['The ship date moved', 'A project you work on moved by more than the threshold.'],
  status: ['Watched issue changed status', 'An issue you watch moved to another column.'],
}

function NotificationSettingsCard() {
  const toast = useToast()
  const [s, setS] = useState<NotificationSettings | null>(null)
  const [hook, setHook] = useState('')
  useEffect(() => {
    api.get<NotificationSettings>('/me/notification-settings').then(setS, () => {})
  }, [])
  useEffect(() => {
    if (s && location.hash === '#notifications') document.getElementById('notifications')?.scrollIntoView({ block: 'start' })
  }, [s])
  if (!s) return null
  const save = (body: { prefs?: Partial<Record<NotificationKind, ChannelPrefs>>; slackWebhookUrl?: string }, done?: string) =>
    api.put<NotificationSettings>('/me/notification-settings', body).then(
      (r) => {
        setS(r)
        if (done) toast(done, 'success')
      },
      (e) => toast(errorMessage(e), 'error'),
    )
  const toggle = (kind: NotificationKind, ch: keyof ChannelPrefs) => {
    const next = { ...s.prefs[kind], [ch]: !s.prefs[kind][ch] }
    setS({ ...s, prefs: { ...s.prefs, [kind]: next } })
    void save({ prefs: { [kind]: next } })
  }
  return (
    <section className="settings-card" id="notifications">
      <h2>
        <Icon name="bell" size={16} /> Notifications
      </h2>
      <p className="muted">
        Choose where each kind of update reaches you. Email and Slack arrive as a short digest rather than one message per change.
      </p>
      <table className="prefs-table">
        <thead>
          <tr>
            <th />
            <th>In app</th>
            <th title={s.emailAvailable ? undefined : 'Email is not configured on this server'}>Email{!s.emailAvailable && ' *'}</th>
            <th title={s.slackConnected ? undefined : 'Connect Slack below'}>Slack{!s.slackConnected && ' *'}</th>
          </tr>
        </thead>
        <tbody>
          {(Object.keys(KIND_LABEL) as NotificationKind[]).map((k) => (
            <tr key={k}>
              <td>
                <b>{KIND_LABEL[k][0]}</b>
                <span className="muted small">{KIND_LABEL[k][1]}</span>
              </td>
              {(['inApp', 'email', 'slack'] as const).map((ch) => (
                <td key={ch} className="prefs-check">
                  <input type="checkbox" checked={s.prefs[k][ch]} onChange={() => toggle(k, ch)} aria-label={`${KIND_LABEL[k][0]}: ${ch}`} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {(!s.emailAvailable || !s.slackConnected) && (
        <p className="muted small">* Not delivered until {!s.emailAvailable ? 'an admin configures email on the server' : 'you connect Slack'}.</p>
      )}
      <form
        className="row slack-row"
        onSubmit={(e) => {
          e.preventDefault()
          void save({ slackWebhookUrl: hook }, 'Slack connected').then(() => setHook(''))
        }}
      >
        {s.slackConnected ? (
          <>
            <span className="small">
              <Icon name="check-circle" size={14} className="text-success" /> Slack is connected.
            </span>
            <Button size="sm" variant="subtle" type="button" onClick={() => void save({ slackWebhookUrl: '' }, 'Slack disconnected')}>
              Disconnect
            </Button>
          </>
        ) : (
          <>
            <input
              className="field-input mono"
              placeholder="https://hooks.slack.com/services/…"
              value={hook}
              onChange={(e) => setHook(e.target.value)}
              aria-label="Slack incoming webhook URL"
            />
            <Button size="sm" type="submit" disabled={!hook.trim()}>
              Connect Slack
            </Button>
          </>
        )}
      </form>
    </section>
  )
}
