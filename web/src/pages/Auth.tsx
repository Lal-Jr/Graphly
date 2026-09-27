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
          <Logo size={32} />
          <span>Graphly</span>
        </div>
        <div>
          <h1>{mode === 'login' ? 'Welcome back' : 'Start planning'}</h1>
          <p className="muted">{mode === 'login' ? 'Log in to pick up where your plan left off.' : 'Create an account — it takes ten seconds.'}</p>
        </div>
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
          Try the live demo — no sign-up
        </Button>
        <p className="auth-switch">
          {mode === 'login' ? "Don't have an account?" : 'Already have an account?'}{' '}
          <Link to={`${other}${next !== '/' ? `?next=${encodeURIComponent(next)}` : ''}`}>{mode === 'login' ? 'Sign up' : 'Log in'}</Link>
        </p>
      </div>
      <aside className="auth-aside">
        <div className="auth-aside-copy">
          <span className="eyebrow">Issue tracking, rewired</span>
          <h2>
            Plan with the graph,
            <br />
            not around it.
          </h2>
          <p>Every “blocks” link feeds a scheduler, so Graphly always knows what’s stuck, who’s holding it up, and when you’ll really ship.</p>
        </div>
        <GraphArt />
        <ul className="auth-points">
          <li>
            <Icon name="route" size={16} /> Critical path &amp; forecast dates
          </li>
          <li>
            <Icon name="target" size={16} /> 85%-confidence ship dates
          </li>
          <li>
            <Icon name="sparkles" size={16} /> “What if it slips?” before it does
          </li>
        </ul>
      </aside>
    </div>
  )
}

/** A small plan drawn as a graph: signals flow along the coral critical path into the ship date. */
function GraphArt() {
  const nodes = [
    { id: 'a', x: 40, y: 150, key: 'APL-4', crit: true },
    { id: 'b', x: 190, y: 70, key: 'APL-5', crit: true },
    { id: 'c', x: 190, y: 230, key: 'APL-7' },
    { id: 'd', x: 340, y: 40, key: 'APL-6', crit: true },
    { id: 'e', x: 340, y: 150, key: 'APL-8' },
    { id: 'f', x: 340, y: 260, key: 'APL-12' },
    { id: 'g', x: 490, y: 100, key: 'APL-9', crit: true },
    { id: 'h', x: 490, y: 220, key: 'APL-13' },
  ]
  const edges: [string, string, boolean?][] = [
    ['a', 'b', true], ['a', 'c'], ['b', 'd', true], ['b', 'e'], ['c', 'e'], ['c', 'f'], ['d', 'g', true], ['e', 'g'], ['f', 'h'], ['e', 'h'],
  ]
  const at = new Map(nodes.map((n) => [n.id, n]))
  const W = 96
  const H = 34
  const path = (a: string, b: string) => {
    const p = at.get(a)!
    const q = at.get(b)!
    const x1 = p.x + W
    const y1 = p.y + H / 2
    const x2 = q.x
    const y2 = q.y + H / 2
    const mx = (x1 + x2) / 2
    return `M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`
  }
  return (
    <div className="graph-art" aria-hidden>
      <svg viewBox="0 0 700 310">
        {edges.map(([a, b, crit]) => (
          <path key={a + b} d={path(a, b)} className={crit ? 'ga-edge crit' : 'ga-edge'} />
        ))}
        <path d={`M${at.get('g')!.x + W},${at.get('g')!.y + H / 2} C620,117 610,160 628,160`} className="ga-edge crit" />
        {edges
          .filter(([, , crit]) => crit)
          .map(([a, b], k) => (
            <circle key={'p' + a + b} r="3.5" className="ga-pulse">
              <animateMotion dur="2.4s" begin={`${k * 0.6}s`} repeatCount="indefinite" path={path(a, b)} />
            </circle>
          ))}
        {nodes.map((n) => (
          <g key={n.id} transform={`translate(${n.x},${n.y})`} className={n.crit ? 'ga-node crit' : 'ga-node'}>
            <rect width={W} height={H} rx="9" />
            <circle cx="14" cy={H / 2} r="4" />
            <text x="26" y={H / 2 + 4}>{n.key}</text>
          </g>
        ))}
        <g transform="translate(628,142)" className="ga-ship">
          <rect width="64" height="36" rx="18" />
          <text x="32" y="23" textAnchor="middle">Ship</text>
        </g>
      </svg>
      <div className="ga-card">
        <span className="eyebrow">Forecast</span>
        <b>20 Oct</b>
        <span>85% sure · 3 on the critical path</span>
      </div>
    </div>
  )
}
