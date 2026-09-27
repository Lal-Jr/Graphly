import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Background,
  Controls,
  Handle,
  MarkerType,
  MiniMap,
  Position,
  ReactFlow,
  applyNodeChanges,
  type Connection,
  type Edge,
  type Node,
  type NodeChange,
  type NodeProps,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import dagre from '@dagrejs/dagre'
import { FilterBar } from '../components/FilterBar'
import { formatDate } from '../lib/forecast'
import { wouldCreateCycle } from '../lib/graph'
import { useFilters } from '../store/filters'
import { useIssueParam } from '../store/nav'
import { useSession } from '../store/session'
import { useTheme } from '../store/theme'
import { useProject, useViewers } from '../store/useProject'
import type { Issue } from '../types'
import { PriorityIcon, TypeIcon } from '../ui/icons'
import { Avatar, AvatarStack, Lozenge } from '../ui/primitives'
import { useToast } from '../ui/toast'

const NODE_W = 240
const NODE_H = 100

type NodeData = { issue: Issue; dim: boolean }
type IssueNode = Node<NodeData, 'issue'>

function IssueNodeView({ data }: NodeProps<IssueNode>) {
  const d = useProject()
  const { user } = useSession()
  const { issue, dim } = data
  const viewers = useViewers(issue.id, user?.id)
  const st = d.statusMap.get(issue.statusId)
  const sched = d.analysis.schedule.get(issue.id)
  const fc = d.forecasts.get(issue.id)
  const critical = d.analysis.criticalSet.has(issue.id)
  const blocked = d.analysis.blockedBy.has(issue.id)
  const cls = ['gnode', `cat-${st?.category}`, critical && 'critical', blocked && 'blocked', dim && 'dim', fc && fc.daysLate > 0 && 'late']
    .filter(Boolean)
    .join(' ')
  return (
    <div className={cls}>
      <Handle type="target" position={Position.Left} />
      <div className="gnode-head">
        <TypeIcon type={issue.type} size={14} />
        <span className="issue-key">{d.keyOf(issue)}</span>
        <span className="spacer" />
        {viewers.length > 0 && <AvatarStack users={viewers.map((v) => ({ ...v, key: v.userId }))} size={16} />}
        <PriorityIcon priority={issue.priority} size={14} />
        <Avatar user={issue.assigneeId ? d.memberMap.get(issue.assigneeId) : null} size={20} />
      </div>
      <div className="gnode-title">{issue.title}</div>
      <div className="gnode-foot">
        {st && <Lozenge category={st.category}>{st.name}</Lozenge>}
        <span>{issue.estimate}d</span>
        {sched && <span className={sched.slack === 0 ? 'text-critical' : ''}>{sched.slack === 0 ? 'no slack' : `${sched.slack}d slack`}</span>}
        {fc && <span className={fc.daysLate > 0 ? 'text-danger' : ''}>→ {formatDate(fc.finish)}</span>}
      </div>
      <Handle type="source" position={Position.Right} />
    </div>
  )
}

const nodeTypes = { issue: IssueNodeView }

function layout(issues: Issue[], edges: [string, string][]) {
  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'LR', nodesep: 24, ranksep: 64, marginx: 24, marginy: 24 })
  g.setDefaultEdgeLabel(() => ({}))
  for (const i of issues) g.setNode(i.id, { width: NODE_W, height: NODE_H })
  for (const [a, b] of edges) g.setEdge(a, b)
  dagre.layout(g)
  return new Map(issues.map((i) => {
    const n = g.node(i.id)
    return [i.id, { x: n.x - NODE_W / 2, y: n.y - NODE_H / 2 }]
  }))
}

export function GraphView() {
  const d = useProject()
  const { match, active } = useFilters()
  const { resolved } = useTheme()
  const toast = useToast()
  const [, openIssue] = useIssueParam()
  const [hideDone, setHideDone] = useState(true)
  const [criticalOnly, setCriticalOnly] = useState(false)
  const [linkedOnly, setLinkedOnly] = useState(false)
  const pinned = useRef(new Map<string, { x: number; y: number }>())
  const [nodes, setNodes] = useState<IssueNode[]>([])

  const linked = useMemo(() => new Set(d.links.flatMap((l) => [l.blockerId, l.blockedId])), [d.links])
  const visible = useMemo(
    () =>
      d.work.filter(
        (i) =>
          (!hideDone || d.category(i) !== 'done') &&
          (!criticalOnly || d.analysis.criticalSet.has(i.id)) &&
          (!linkedOnly || linked.has(i.id)),
      ),
    [d, hideDone, criticalOnly, linkedOnly, linked],
  )
  const ids = useMemo(() => new Set(visible.map((i) => i.id)), [visible])
  const edgePairs = useMemo(
    () => d.links.filter((l) => ids.has(l.blockerId) && ids.has(l.blockedId)).map((l) => [l.blockerId, l.blockedId] as [string, string]),
    [d.links, ids],
  )
  const structure = visible.map((i) => i.id).join() + '|' + edgePairs.map((e) => e.join('>')).join()
  const positions = useMemo(() => layout(visible, edgePairs), [structure]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    setNodes((prev) => {
      const selected = new Set(prev.filter((n) => n.selected).map((n) => n.id))
      return visible.map((i) => ({
        id: i.id,
        type: 'issue',
        position: pinned.current.get(i.id) ?? positions.get(i.id)!,
        selected: selected.has(i.id),
        data: { issue: i, dim: active && !match(i) },
      }))
    })
  }, [visible, positions, match, active])

  const edges = useMemo<Edge[]>(() => {
    const cp = d.analysis.criticalPath
    const critical = new Set(cp.slice(1).map((id, k) => `${cp[k]}>${id}`))
    return edgePairs.map(([a, b]) => {
      const id = `${a}>${b}`
      const isCrit = critical.has(id)
      const open = d.category(d.issueMap.get(a)!) !== 'done'
      return {
        id,
        source: a,
        target: b,
        animated: isCrit,
        className: isCrit ? 'edge-critical' : open ? 'edge-open' : 'edge-done',
        markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
      }
    })
  }, [edgePairs, d])

  const onNodesChange = useCallback((changes: NodeChange<IssueNode>[]) => {
    for (const c of changes) if (c.type === 'position' && c.position) pinned.current.set(c.id, c.position)
    // Issues are deleted from the issue view, never by a stray Backspace on the canvas.
    setNodes((ns) => applyNodeChanges(changes.filter((c) => c.type !== 'remove'), ns))
  }, [])

  const onConnect = useCallback(
    ({ source, target }: Connection) => {
      if (!source || !target) return
      if (wouldCreateCycle(d.nodes, target, source)) {
        toast('That link would create a dependency cycle', 'warning')
        return
      }
      void d.store.addBlocker(target, source)
    },
    [d, toast],
  )

  return (
    <div className="view view-graph">
      <FilterBar>
        <label className="toggle-inline">
          <input type="checkbox" checked={hideDone} onChange={(e) => setHideDone(e.target.checked)} /> Hide done
        </label>
        <label className="toggle-inline">
          <input type="checkbox" checked={linkedOnly} onChange={(e) => setLinkedOnly(e.target.checked)} /> Linked only
        </label>
        <label className="toggle-inline">
          <input type="checkbox" checked={criticalOnly} onChange={(e) => setCriticalOnly(e.target.checked)} /> Critical path
        </label>
        <button
          className="btn btn-default btn-sm"
          onClick={() => {
            pinned.current.clear()
            setNodes((ns) => ns.map((n) => ({ ...n, position: positions.get(n.id) ?? n.position })))
          }}
        >
          Re-layout
        </button>
      </FilterBar>
      <div className="graph-canvas">
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          onNodesChange={onNodesChange}
          onConnect={onConnect}
          onEdgesDelete={(es) => es.forEach((e) => d.store.removeBlocker(e.target, e.source))}
          deleteKeyCode={['Backspace', 'Delete']}
          onNodeClick={(_, n) => openIssue(d.keyOf((n.data as NodeData).issue))}
          fitView
          fitViewOptions={{ padding: 0.15, maxZoom: 1 }}
          minZoom={0.15}
          proOptions={{ hideAttribution: true }}
          colorMode={resolved}
        >
          <Background gap={20} size={1} />
          <Controls showInteractive={false} />
          <MiniMap pannable zoomable nodeClassName={(n) => (d.analysis.criticalSet.has(n.id) ? 'mm-critical' : 'mm-node')} />
        </ReactFlow>
        <div className="graph-legend">
          <span>
            <i className="legend-line crit" /> Critical path
          </span>
          <span>
            <i className="legend-line open" /> Open dependency
          </span>
          <span>
            <i className="legend-line done" /> Satisfied
          </span>
          <span className="muted">Drag between handles to link · select a link and press ⌫ to remove</span>
        </div>
      </div>
    </div>
  )
}
