import { useState } from 'react'
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router-dom'
import { api, errorMessage } from '../api'
import { useSession } from '../store/session'
import type { User } from '../types'
import { Icon, Logo } from '../ui/icons'
import { Button } from '../ui/primitives'

export function AuthPage({ mode }: { mode: 'login' | 'signup' }) {
  const { user, refresh } = useSession()
  const [params] = useSearchParams()
  const next = params.get('next') || '/'
  const nav = useNavigate()
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  // Where to land once signed in; the demo sends you straight to its board.
  const [target, setTarget] = useState(next)

  if (user) return <Navigate to={target} replace />

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await api.post<User>(mode === 'login' ? '/auth/login' : '/auth/signup', mode === 'login' ? { email, password } : { name, email, password })
      await refresh()
      nav(next, { replace: true })
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }
  const other = mode === 'login' ? '/signup' : '/login'

  return (
    <div className="auth">
      <div className="auth-card">
        <div className="auth-brand">
          <Logo size={36} />
          <span>Graphly</span>
        </div>
        <h1>{mode === 'login' ? 'Log in to continue' : 'Create your account'}</h1>
        <form onSubmit={submit} className="auth-form">
          {mode === 'signup' && (
            <label className="stack-field">
              Full name
              <input className="field-input" autoComplete="name" required value={name} onChange={(e) => setName(e.target.value)} />
            </label>
          )}
          <label className="stack-field">
            Email
            <input className="field-input" type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
          </label>
          <label className="stack-field">
            Password
            <input
              className="field-input"
              type="password"
              autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
              required
              minLength={mode === 'signup' ? 8 : undefined}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            {mode === 'signup' && <span className="muted small">At least 8 characters.</span>}
          </label>
          {error && (
            <div className="form-error" role="alert">
              <Icon name="warning" size={14} /> {error}
            </div>
          )}
          <Button type="submit" variant="primary" disabled={busy} className="btn-block">
            {mode === 'login' ? 'Log in' : 'Sign up'}
          </Button>
        </form>
        <div className="auth-divider">
          <span>or</span>
        </div>
        <Button
          className="btn-block demo-btn"
          icon={<Icon name="sparkles" size={16} />}
          disabled={busy}
          onClick={async () => {
            setBusy(true)
            setError(null)
            try {
              const r = await api.post<{ projectId: string }>('/auth/demo')
              setTarget(`/p/${r.projectId}/board`)
              await refresh()
            } catch (err) {
              setError(errorMessage(err))
              setBusy(false)
            }
          }}
        >
          Explore the demo — no sign-up
        </Button>
        <p className="auth-switch">
          {mode === 'login' ? "Don't have an account?" : 'Already have an account?'}{' '}
          <Link to={`${other}${next !== '/' ? `?next=${encodeURIComponent(next)}` : ''}`}>{mode === 'login' ? 'Sign up' : 'Log in'}</Link>
        </p>
      </div>
      <aside className="auth-aside">
        <h2>Plan with the graph, not around it.</h2>
        <ul>
          <li>
            <Icon name="flame" size={18} /> Critical path and forecast dates, computed from your dependencies
          </li>
          <li>
            <Icon name="block" size={18} /> Blockers ranked by how much work they hold up
          </li>
          <li>
            <Icon name="sparkles" size={18} /> “What if this slips?” simulation before it happens
          </li>
          <li>
            <Icon name="users" size={18} /> Real-time boards with live presence
          </li>
        </ul>
      </aside>
    </div>
  )
}
