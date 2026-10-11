import { useEffect, useState } from 'react'
import { api, errorMessage } from '../api'
import { Sidebar } from '../components/Sidebar'
import { timeAgo } from '../lib/time'
import { useSession } from '../store/session'
import { useTheme, type ThemeMode } from '../store/theme'
import type { ApiToken, ChannelPrefs, NotificationKind, NotificationSettings, User } from '../types'
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
          <SignInCard />
          <NotificationSettingsCard />
          <ApiTokensCard />
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

const PROVIDER_LABEL: Record<string, string> = { google: 'Google', github: 'GitHub' }

/** Password and connected Google/GitHub accounts. */
function SignInCard() {
  const toast = useToast()
  const [data, setData] = useState<{ identities: { provider: string; email: string }[]; hasPassword: boolean } | null>(null)
  const [providers, setProviders] = useState<{ id: string; label: string }[]>([])
  const [pw, setPw] = useState({ current: '', password: '' })
  const [editing, setEditing] = useState(false)
  const load = () => api.get<typeof data>('/me/identities').then(setData, () => {})
  useEffect(() => {
    void load()
    api.get<{ id: string; label: string }[]>('/auth/providers').then(setProviders, () => {})
    const connected = new URLSearchParams(location.search).get('connected')
    if (connected) toast(`${PROVIDER_LABEL[connected] ?? connected} connected`, 'success')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  if (!data) return null
  const linked = new Map(data.identities.map((i) => [i.provider, i]))
  const methods = data.identities.length + (data.hasPassword ? 1 : 0)
  return (
    <section className="settings-card" id="sign-in">
      <h2>
        <Icon name="lock" size={18} /> Sign-in
      </h2>
      <div className="signin-row">
        <span>
          <b>Password</b>
          <span className="muted small">{data.hasPassword ? 'Set' : 'Not set: you sign in with ' + data.identities.map((i) => PROVIDER_LABEL[i.provider]).join(' or ')}</span>
        </span>
        {!editing && (
          <Button size="sm" onClick={() => setEditing(true)}>
            {data.hasPassword ? 'Change' : 'Set a password'}
          </Button>
        )}
      </div>
      {editing && (
        <form
          className="settings-form"
          onSubmit={async (e) => {
            e.preventDefault()
            try {
              await api.put('/me/password', pw)
              toast('Password saved. Other sessions were signed out.', 'success')
              setEditing(false)
              setPw({ current: '', password: '' })
              void load()
            } catch (err) {
              toast(errorMessage(err), 'error')
            }
          }}
        >
          {data.hasPassword && (
            <label className="stack-field">
              Current password
              <input className="field-input" type="password" autoComplete="current-password" required value={pw.current} onChange={(e) => setPw({ ...pw, current: e.target.value })} />
            </label>
          )}
          <label className="stack-field">
            New password
            <input className="field-input" type="password" autoComplete="new-password" minLength={8} required value={pw.password} onChange={(e) => setPw({ ...pw, password: e.target.value })} />
          </label>
          <div className="row signin-actions">
            <Button type="submit" variant="primary">
              Save password
            </Button>
            <Button type="button" variant="subtle" onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </div>
        </form>
      )}
      {['google', 'github']
        .filter((id) => linked.has(id) || providers.some((p) => p.id === id))
        .map((id) => {
          const ident = linked.get(id)
          return (
            <div className="signin-row" key={id}>
              <span>
                <b>{PROVIDER_LABEL[id]}</b>
                <span className="muted small">{ident ? ident.email : 'Not connected'}</span>
              </span>
              {ident ? (
                <Button
                  size="sm"
                  variant="subtle"
                  disabled={methods < 2}
                  title={methods < 2 ? 'Set a password first so you can still sign in' : undefined}
                  onClick={() =>
                    api.del(`/me/identities/${id}`).then(
                      () => load(),
                      (e) => toast(errorMessage(e), 'error'),
                    )
                  }
                >
                  Disconnect
                </Button>
              ) : (
                <a className="btn btn-default btn-sm" href={`/api/auth/oauth/${id}/start?link=1`}>
                  Connect
                </a>
              )}
            </div>
          )
        })}
    </section>
  )
}

/** Personal API tokens: create (shown once), list with last use, revoke. */
function ApiTokensCard() {
  const toast = useToast()
  const [tokens, setTokens] = useState<ApiToken[] | null>(null)
  const [draft, setDraft] = useState({ name: '', scope: 'write' as ApiToken['scope'], expiresInDays: '' })
  const [fresh, setFresh] = useState<string | null>(null)
  const load = () => api.get<ApiToken[]>('/me/tokens').then(setTokens, () => setTokens([]))
  useEffect(() => {
    void load()
  }, [])
  return (
    <section className="settings-card" id="api-tokens">
      <h2>
        <Icon name="key" size={18} /> API tokens
      </h2>
      <p className="muted small">
        Tokens let scripts use the <a href="/api/openapi.yaml" target="_blank" rel="noreferrer">Graphly API</a> as you. Send one as{' '}
        <code>Authorization: Bearer gly_…</code>. Read tokens can only fetch.
      </p>
      {fresh && (
        <div className="token-fresh" role="status">
          <b>Copy your new token now. You won't see it again.</b>
          <div className="row">
            <code className="token-value">{fresh}</code>
            <Button
              size="sm"
              icon={<Icon name="copy" size={14} />}
              onClick={() => navigator.clipboard.writeText(fresh).then(() => toast('Copied', 'success'))}
            >
              Copy
            </Button>
          </div>
        </div>
      )}
      <form
        className="row token-form"
        onSubmit={async (e) => {
          e.preventDefault()
          try {
            const r = await api.post<{ token: string }>('/me/tokens', {
              name: draft.name,
              scope: draft.scope,
              expiresInDays: draft.expiresInDays ? Number(draft.expiresInDays) : undefined,
            })
            setFresh(r.token)
            setDraft({ ...draft, name: '' })
            void load()
          } catch (err) {
            toast(errorMessage(err), 'error')
          }
        }}
      >
        <input className="field-input" placeholder="Token name, e.g. CI sync" maxLength={60} required value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
        <select className="field-input" value={draft.scope} onChange={(e) => setDraft({ ...draft, scope: e.target.value as ApiToken['scope'] })} aria-label="Access">
          <option value="write">Read and write</option>
          <option value="read">Read only</option>
        </select>
        <select className="field-input" value={draft.expiresInDays} onChange={(e) => setDraft({ ...draft, expiresInDays: e.target.value })} aria-label="Expires">
          <option value="">Never expires</option>
          <option value="30">30 days</option>
          <option value="90">90 days</option>
          <option value="365">1 year</option>
        </select>
        <Button type="submit" variant="primary" disabled={!draft.name.trim()}>
          Create token
        </Button>
      </form>
      {tokens?.map((t) => (
        <div className="signin-row" key={t.id}>
          <span>
            <b>{t.name}</b>
            <span className="muted small">
              <span className="mono">{t.prefix}…</span> · {t.scope === 'read' ? 'read only' : 'read and write'} · created {timeAgo(t.createdAt)} ·{' '}
              {t.lastUsedAt ? `last used ${timeAgo(t.lastUsedAt)}` : 'never used'}
              {t.expiresAt && ` · expires ${new Date(t.expiresAt).toLocaleDateString()}`}
            </span>
          </span>
          <Button
            size="sm"
            variant="subtle"
            onClick={() =>
              api.del(`/me/tokens/${t.id}`).then(
                () => load(),
                (e) => toast(errorMessage(e), 'error'),
              )
            }
          >
            Revoke
          </Button>
        </div>
      ))}
    </section>
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
