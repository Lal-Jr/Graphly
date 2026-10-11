import { useState } from 'react'
import { api, errorMessage } from '../api'
import type { ImportReport } from '../types'
import { Icon } from '../ui/icons'
import { Button, Modal } from '../ui/primitives'
import { useToast } from '../ui/toast'

const MAX_BYTES = 25 << 20

/** Two steps: pick an export and preview what it would do (a dry run on the server), then import. */
export function ImportDialog({ workspaceId, onClose, onDone }: { workspaceId: string; onClose: () => void; onDone: (r: ImportReport) => void }) {
  const toast = useToast()
  const [source, setSource] = useState<'jira' | 'linear'>('jira')
  const [file, setFile] = useState<{ name: string; content: string } | null>(null)
  const [preview, setPreview] = useState<ImportReport | null>(null)
  const [busy, setBusy] = useState(false)

  const run = async (dryRun: boolean) => {
    if (!file) return
    setBusy(true)
    try {
      const r = await api.post<ImportReport>(`/workspaces/${workspaceId}/import`, { source, content: file.content, dryRun })
      if (dryRun) setPreview(r)
      else onDone(r)
    } catch (e) {
      toast(errorMessage(e), 'error')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal onClose={onClose} label="Import" width={600}>
      <div className="modal-body import-dialog">
        <h2>Import from Jira or Linear</h2>
        {!preview ? (
          <>
            <div className="segmented" role="radiogroup" aria-label="Source">
              {(['jira', 'linear'] as const).map((s) => (
                <button key={s} type="button" role="radio" aria-checked={source === s} className={source === s ? 'on' : ''} onClick={() => setSource(s)}>
                  {s === 'jira' ? 'Jira' : 'Linear'}
                </button>
              ))}
            </div>
            <p className="muted small">
              {source === 'jira'
                ? 'In Jira, export issues as CSV (all fields), or save the REST search response as JSON. Blocks links, epics, comments, story points and due dates come across.'
                : 'In Linear, use Settings → Import/Export → Export CSV, or save a GraphQL issues query as JSON. Only the JSON includes comments; both include blocking relations if your export has them.'}
            </p>
            <label className="import-drop">
              <Icon name="upload" size={20} />
              <span>{file ? <b>{file.name}</b> : 'Choose a .csv or .json export'}</span>
              <input
                type="file"
                accept=".csv,.json,text/csv,application/json"
                onChange={async (e) => {
                  const f = e.target.files?.[0]
                  if (!f) return
                  if (f.size > MAX_BYTES) return toast('Exports are limited to 25 MB. Split it by project.', 'error')
                  setFile({ name: f.name, content: await f.text() })
                }}
              />
            </label>
            <p className="muted small">People are matched to workspace members by email, then by name. Re-importing the same export updates what it created instead of duplicating it.</p>
          </>
        ) : (
          <ImportPreview r={preview} />
        )}
        <div className="modal-actions">
          <Button type="button" variant="subtle" onClick={preview ? () => setPreview(null) : onClose}>
            {preview ? 'Back' : 'Cancel'}
          </Button>
          {preview ? (
            <Button variant="primary" disabled={busy} onClick={() => run(false)}>
              Import {preview.issues.total} issues
            </Button>
          ) : (
            <Button variant="primary" disabled={busy || !file} onClick={() => run(true)}>
              Preview
            </Button>
          )}
        </div>
      </div>
    </Modal>
  )
}

const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`

function entries(m: Record<string, number>) {
  return Object.entries(m).sort((a, b) => b[1] - a[1])
}

export function ImportPreview({ r }: { r: ImportReport }) {
  const unmapped = [
    ...entries(r.unmapped.types).map(([k, n]) => `Type "${k || '(none)'}" on ${plural(n, 'issue')} → task`),
    ...entries(r.unmapped.priorities).map(([k, n]) => `Priority "${k}" on ${plural(n, 'issue')} → medium`),
    ...entries(r.unmapped.people).map(([k, n]) => `${k} isn't a member here (${plural(n, 'reference')} left unassigned)`),
    ...(r.unmapped.parents ? [`${plural(r.unmapped.parents, 'parent link')} to non-epics (sub-tasks) not kept`] : []),
  ]
  return (
    <div className="import-preview">
      <div className="import-stats">
        <span>
          <b>{r.issues.created}</b> new {r.issues.created === 1 ? 'issue' : 'issues'}
        </span>
        <span>
          <b>{r.issues.updated}</b> updated
        </span>
        {r.issues.unchanged > 0 && (
          <span>
            <b>{r.issues.unchanged}</b> unchanged
          </span>
        )}
        <span>
          <b>{r.links.created}</b> blocking {r.links.created === 1 ? 'link' : 'links'}
        </span>
        <span>
          <b>{r.comments.created}</b> {r.comments.created === 1 ? 'comment' : 'comments'}
        </span>
      </div>
      <ul className="import-list">
        {r.projects.map((p) => (
          <li key={p.key}>
            <span className="mono">{p.key}</span> {p.name}{' '}
            <span className="muted small">{p.action === 'create' ? '· new project' : '· existing project'}</span>
          </li>
        ))}
        {r.statusesCreated.map((s) => (
          <li key={s.project + s.name} className="muted small">
            New status "{s.name}" in {s.project} ({s.category.replace('_', ' ')})
          </li>
        ))}
      </ul>
      {r.links.droppedCount > 0 && (
        <details className="import-section warn" open={r.links.droppedCount <= 5}>
          <summary>
            <Icon name="warning" size={14} /> {plural(r.links.droppedCount, 'link')} won't be imported
          </summary>
          <ul>
            {r.links.dropped.map((d) => (
              <li key={d.blocker + d.blocked}>
                <span className="mono">{d.blocker}</span> blocks <span className="mono">{d.blocked}</span>: {d.reason}
              </li>
            ))}
            {r.links.droppedCount > r.links.dropped.length && <li className="muted">…and {r.links.droppedCount - r.links.dropped.length} more</li>}
          </ul>
        </details>
      )}
      {unmapped.length > 0 && (
        <details className="import-section">
          <summary>{plural(unmapped.length, 'value')} without a Graphly match</summary>
          <ul>
            {unmapped.map((u) => (
              <li key={u}>{u}</li>
            ))}
          </ul>
        </details>
      )}
      {r.warnings.map((w) => (
        <p key={w} className="muted small">
          {w}
        </p>
      ))}
    </div>
  )
}
