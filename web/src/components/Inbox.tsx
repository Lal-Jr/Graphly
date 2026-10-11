import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api'
import { formatDate } from '../lib/forecast'
import { timeAgo } from '../lib/time'
import type { AppNotification } from '../types'
import { Icon, type IconName } from '../ui/icons'
import { Popover, Spinner } from '../ui/primitives'

const POLL_MS = 30_000

const KIND_ICON: Record<AppNotification['kind'], IconName> = {
  unblocked: 'zap',
  late: 'clock',
  mention: 'user',
  status: 'arrow-right',
  date_moved: 'target',
}

/** One line saying what happened, in plain words. */
export function describe(n: AppNotification): string {
  const d = n.data as Record<string, any> // eslint-disable-line @typescript-eslint/no-explicit-any
  const who = d.actorName ?? 'Someone'
  switch (n.kind) {
    case 'unblocked':
      return `${d.key} is unblocked: ${d.projectKey}-${d.byNumber} ${d.byTitle} is done`
    case 'late': {
      const issues = (d.issues ?? []) as { number: number; daysLate: number }[]
      return issues.length === 1
        ? `${d.projectKey}-${issues[0].number} is now forecast ${issues[0].daysLate}d past its due date`
        : `${issues.length} of your issues are now forecast past their due dates`
    }
    case 'mention':
      return `${who} mentioned you on ${d.key}${d.in === 'comment' ? '' : ` (${d.in})`}`
    case 'status':
      return `${who} moved ${d.key} from ${d.from} to ${d.to}`
    case 'date_moved':
      return `${d.projectName} now ships ${formatDate(String(d.to))} (${Number(d.days) > 0 ? '+' : ''}${d.days} working days)`
  }
}

/** The bell: unread count, polled, with the latest notifications in a popover. */
export function Inbox() {
  const nav = useNavigate()
  const [unread, setUnread] = useState(0)
  const [items, setItems] = useState<AppNotification[] | null>(null)

  const poll = useCallback(() => {
    if (document.hidden) return
    api.get<{ unread: number }>('/notifications/unread').then((r) => setUnread(r.unread), () => {})
  }, [])
  useEffect(() => {
    poll()
    const t = setInterval(poll, POLL_MS)
    document.addEventListener('visibilitychange', poll)
    return () => {
      clearInterval(t)
      document.removeEventListener('visibilitychange', poll)
    }
  }, [poll])

  const load = () =>
    api.get<{ items: AppNotification[]; unread: number }>('/notifications').then((r) => {
      setItems(r.items)
      setUnread(r.unread)
    }, () => setItems([]))

  const markRead = (body: { ids?: number[]; all?: boolean }) =>
    api.post<{ unread: number }>('/notifications/read', body).then((r) => {
      setUnread(r.unread)
      setItems((xs) => xs?.map((x) => (body.all || body.ids?.includes(x.id) ? { ...x, readAt: x.readAt ?? new Date().toISOString() } : x)) ?? null)
    }, () => {})

  return (
    <Popover
      align="start"
      width={360}
      className="inbox-pop"
      trigger={({ toggle, ref, open }) => (
        <button
          ref={ref}
          className={`icon-btn sm inbox-btn${open ? ' open' : ''}`}
          onClick={() => {
            if (!open) void load()
            toggle()
          }}
          title={unread ? `${unread} unread notifications` : 'Notifications'}
          aria-label="Notifications"
        >
          <Icon name="bell" size={16} />
          {unread > 0 && <span className="inbox-badge">{unread > 99 ? '99+' : unread}</span>}
        </button>
      )}
    >
      {(close) => (
        <div className="inbox">
          <header>
            <b>Notifications</b>
            <span className="spacer" />
            {unread > 0 && (
              <button className="link-btn small" onClick={() => void markRead({ all: true })}>
                Mark all read
              </button>
            )}
            <button
              className="icon-btn sm"
              title="Notification settings"
              onClick={() => {
                close()
                nav('/profile#notifications')
              }}
            >
              <Icon name="settings" size={14} />
            </button>
          </header>
          {items === null ? (
            <div className="inbox-empty">
              <Spinner size={18} />
            </div>
          ) : items.length === 0 ? (
            <div className="inbox-empty muted">
              <Icon name="bell-off" size={22} />
              Nothing yet. You'll hear when you're unblocked, mentioned, or your work slips.
            </div>
          ) : (
            <ul>
              {items.map((n) => (
                <li key={n.id}>
                  <button
                    className={`inbox-item${n.readAt ? '' : ' unread'} kind-${n.kind}`}
                    onClick={() => {
                      if (!n.readAt) void markRead({ ids: [n.id] })
                      close()
                      const key = typeof n.data.key === 'string' ? `?issue=${n.data.key}` : ''
                      if (n.projectId) nav(`/p/${n.projectId}/${n.kind === 'date_moved' || n.kind === 'late' ? 'insights' : 'board'}${key}`)
                    }}
                  >
                    <span className="inbox-icon">
                      <Icon name={KIND_ICON[n.kind]} size={14} />
                    </span>
                    <span className="inbox-text">
                      <span>{describe(n)}</span>
                      {typeof n.data.title === 'string' && n.kind !== 'late' && <span className="muted small truncate">{n.data.title}</span>}
                      {n.kind === 'mention' && typeof n.data.excerpt === 'string' && <span className="inbox-quote">{n.data.excerpt}</span>}
                      <span className="muted small">
                        {String(n.data.projectKey ?? '')} · {timeAgo(n.createdAt)}
                      </span>
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </Popover>
  )
}
