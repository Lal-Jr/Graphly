import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, errorMessage } from '../api'
import { useSession } from '../store/session'
import { Logo } from '../ui/icons'
import { Button, Spinner } from '../ui/primitives'

export function InvitePage() {
  const { token } = useParams()
  const { user, refresh, setWorkspace } = useSession()
  const nav = useNavigate()
  const [info, setInfo] = useState<{ workspace: string; invitedBy: string } | null | 'invalid'>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    api.get<{ workspace: string; invitedBy: string }>(`/invites/${token}`).then(setInfo, () => setInfo('invalid'))
  }, [token])

  const accept = async () => {
    try {
      const r = await api.post<{ workspaceId: string }>(`/invites/${token}/accept`)
      await refresh()
      setWorkspace(r.workspaceId)
      nav('/', { replace: true })
    } catch (e) {
      setError(errorMessage(e))
    }
  }
  const here = encodeURIComponent(`/invite/${token}`)

  return (
    <div className="auth auth-single">
      <div className="auth-card">
        <div className="auth-brand">
          <Logo size={36} />
          <span>Graphly</span>
        </div>
        {info === null ? (
          <Spinner />
        ) : info === 'invalid' ? (
          <>
            <h1>This invite has expired</h1>
            <p className="muted">Invite links last 7 days. Ask for a fresh one.</p>
            <Link to="/">Go to Graphly</Link>
          </>
        ) : (
          <>
            <h1>Join {info.workspace}</h1>
            <p className="muted">{info.invitedBy} invited you to collaborate.</p>
            {error && <div className="form-error">{error}</div>}
            {user ? (
              <Button variant="primary" className="btn-block" onClick={accept}>
                Join as {user.name}
              </Button>
            ) : (
              <div className="stack">
                <Link className="btn btn-primary btn-md btn-block" to={`/signup?next=${here}`}>
                  Create an account
                </Link>
                <Link className="btn btn-default btn-md btn-block" to={`/login?next=${here}`}>
                  I already have an account
                </Link>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
