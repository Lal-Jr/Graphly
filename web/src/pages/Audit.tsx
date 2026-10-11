import { useCallback, useEffect, useState } from 'react'
import { api, errorMessage } from '../api'
import { Sidebar } from '../components/Sidebar'
import { timeAgo } from '../lib/time'
import { useSession } from '../store/session'
import type { AuditEntry } from '../types'
import { Icon } from '../ui/icons'
import { Button, EmptyState, Spinner } from '../ui/primitives'

const ACTION_LABEL: Record<string, string> = {
  'member.join': 'joined the workspace',
  'member.role': 'changed a workspace role',
  'member.remove': 'removed a member',
  'member.leave': 'left the workspace',
  'invite.create': 'created an invite link',
  'project.member.role': 'changed a project role',
  'project.delete': 'deleted a project',
  'issue.delete': 'deleted an issue',
  'status.delete': 'deleted a status',
  'comment.delete': "deleted someone's comment",
  'token.create': 'created an API token',
  'token.revoke': 'revoked an API token',
  'identity.link': 'connected a sign-in method',
  'identity.unlink': 'disconnected a sign-in method',
  'password.set': 'set their password',
  'webhook.github.rotate': 'generated a GitHub webhook secret',
  'webhook.github.disable': 'turned off the GitHub webhook',
  'webhook.gitlab.rotate': 'generated a GitLab webhook secret',
  'webhook.gitlab.disable': 'turned off the GitLab webhook',
  import: 'ran an import',
}

/** Who did what, when: a short line per entry plus the details the server recorded. */
export function describeAudit(e: AuditEntry, nameOf: (id: string) => string | undefined): string {
  const d = e.data as Record<string, unknown>
  const target = e.targetType === 'user' && e.targetId ? (nameOf(e.targetId) ?? 'a former member') : null
  switch (e.action) {
    case 'member.role':
      return `changed ${target}'s workspace role from ${d.from} to ${d.to}`
    case 'member.remove':
      return `removed ${target}`
    case 'project.member.role':
      return `set ${target}'s project role to ${d.to ?? 'the default'}`
    case 'invite.create':
      return `created an invite link for a${d.role === 'admin' ? 'n' : ''} ${d.role}`
    case 'member.join':
      return `joined as ${d.role}`
    case 'issue.delete':
      return `deleted issue #${d.number} "${d.title}"`
    case 'project.delete':
      return `deleted project ${d.key} "${d.name}"`
    case 'token.create':
      return `created API token "${d.name}" (${d.scope})`
    case 'import':
      return `imported from ${d.source}`
  }
  return ACTION_LABEL[e.action] ?? e.action
}

export function AuditPage() {
  const { workspace } = useSession()
  const [entries, setEntries] = useState<AuditEntry[] | null>(null)
  const [names, setNames] = useState<Map<string, string>>(new Map())
  const [error, setError] = useState<string | null>(null)
  const [more, setMore] = useState(true)

  const load = useCallback(
    async (before?: number) => {
      if (!workspace) return
      try {
        const page = await api.get<AuditEntry[]>(`/workspaces/${workspace.id}/audit${before ? `?before=${before}` : ''}`)
        setEntries((cur) => (before ? [...(cur ?? []), ...page] : page))
        setMore(page.length === 100)
      } catch (e) {
        setError(errorMessage(e))
      }
    },
    [workspace],
  )
  useEffect(() => {
    void load()
    if (workspace)
      api.get<{ id: string; name: string }[]>(`/workspaces/${workspace.id}/members`).then(
        (ms) => setNames(new Map(ms.map((m) => [m.id, m.name]))),
        () => {},
      )
  }, [load, workspace])

  return (
    <div className="app">
      <Sidebar />
      <main className="app-main home">
        <div className="home-inner narrow">
          <h1>Audit log</h1>
          <p className="muted">
            Sensitive actions in {workspace?.name}, newest first. Entries can't be edited or deleted.
          </p>
          {error ? (
            <EmptyState icon={<Icon name="lock" size={32} />} title="Only workspace admins can see the audit log">
              <p className="muted">{error}</p>
            </EmptyState>
          ) : entries === null ? (
            <Spinner />
          ) : entries.length === 0 ? (
            <p className="muted">Nothing recorded yet.</p>
          ) : (
            <section className="settings-card audit-list">
              {entries.map((e) => (
                <div key={e.id} className="audit-row">
                  <span className="audit-when mono small" title={new Date(e.createdAt).toLocaleString()}>
                    {timeAgo(e.createdAt)}
                  </span>
                  <span>
                    <b>{e.actorName ?? 'Someone'}</b> {describeAudit(e, (id) => names.get(id))}
                  </span>
                  {e.ip && <span className="muted small mono">{e.ip}</span>}
                </div>
              ))}
              {more && (
                <Button variant="subtle" onClick={() => load(entries[entries.length - 1].id)}>
                  Load older
                </Button>
              )}
            </section>
          )}
        </div>
      </main>
    </div>
  )
}
