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
import dagre from '@dagrejs/dagre'
import { wouldCreateCycle } from '../lib/graph'
import { COLUMNS, type Task } from '../lib/types'
import { useBoard, useViewers } from '../store/context'
import { Avatar, useToast } from './ui'

const NODE_W = 220
const NODE_H = 84
const STATUS_LABEL = Object.fromEntries(COLUMNS.map((c) => [c.id, c.label]))

type TaskNodeData = { task: Task; critical: boolean; blocked: boolean; cyclic: boolean; slack?: number; dim: boolean }
type TaskNodeType = Node<TaskNodeData, 'task'>

function TaskNode({ data }: NodeProps<TaskNodeType>) {
  const { task, critical, blocked, cyclic, slack, dim } = data
  const viewers = useViewers(task.id)
  const cls = ['gnode', `s-${task.status}`, critical && 'critical', blocked && 'blocked', cyclic && 'cyclic', dim && 'dim']
    .filter(Boolean)
    .join(' ')
  return (
    <div className={cls}>
      <Handle type="target" position={Position.Left} />
      <div className="gnode-title">
        <span className={`prio prio-${task.priority}`} />
        {task.title || <em>Untitled</em>}
      </div>
      <div className="gnode-meta">
        <span className={`pill s-${task.status}`}>{STATUS_LABEL[task.status]}</span>
        <span>{task.estimate}d</span>
        {slack !== undefined && task.status !== 'done' && <span title="Slack: how long this can slip without delaying the project">{slack === 0 ? 'no slack' : `+${slack}d slack`}</span>}
        {viewers.map((v) => (
          <Avatar key={v.clientId} user={v} size={16} ring />
        ))}
      </div>
      <Handle type="source" position={Position.Right} />
    </div>
  )
}

const nodeTypes = { task: TaskNode }

function layout(tasks: Task[]): Map<string, { x: number; y: number }> {
  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'LR', nodesep: 28, ranksep: 70, marginx: 20, marginy: 20 })
  g.setDefaultEdgeLabel(() => ({}))
  const ids = new Set(tasks.map((t) => t.id))
  for (const t of tasks) g.setNode(t.id, { width: NODE_W, height: NODE_H })
  for (const t of tasks) for (const d of t.dependsOn) if (ids.has(d)) g.setEdge(d, t.id)
  dagre.layout(g)
  const out = new Map<string, { x: number; y: number }>()
  for (const t of tasks) {
    const n = g.node(t.id)
    out.set(t.id, { x: n.x - NODE_W / 2, y: n.y - NODE_H / 2 })
  }
  return out
}

export function GraphView({ filter, onOpen }: { filter: (t: Task) => boolean; onOpen: (id: string) => void }) {
  const { board, tasks, analysis } = useBoard()
  const toast = useToast()
  const [hideDone, setHideDone] = useState(false)
  const [criticalOnly, setCriticalOnly] = useState(false)
  // Positions the user has dragged by hand survive re-layouts until reset.
  const pinned = useRef(new Map<string, { x: number; y: number }>())
  const [nodes, setNodes] = useState<TaskNodeType[]>([])

  const visible = useMemo(
    () => tasks.filter((t) => (!hideDone || t.status !== 'done') && (!criticalOnly || analysis.criticalSet.has(t.id))),
    [tasks, hideDone, criticalOnly, analysis],
  )
  const structureKey = visible.map((t) => `${t.id}:${t.dependsOn.join(',')}`).join('|')
  const positions = useMemo(() => layout(visible), [structureKey]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    setNodes((prev) => {
      const selected = new Set(prev.filter((n) => n.selected).map((n) => n.id))
      return visible.map((t) => ({
        id: t.id,
        type: 'task',
        position: pinned.current.get(t.id) ?? positions.get(t.id)!,
        selected: selected.has(t.id),
        data: {
          task: t,
          critical: analysis.criticalSet.has(t.id),
          blocked: analysis.blockedBy.has(t.id),
          cyclic: analysis.inCycle.has(t.id),
          slack: analysis.schedule.get(t.id)?.slack,
          dim: !filter(t),
        },
      }))
    })
  }, [visible, positions, analysis, filter])

  const edges = useMemo<Edge[]>(() => {
    const ids = new Set(visible.map((t) => t.id))
    const byId = new Map(tasks.map((t) => [t.id, t]))
    const cp = analysis.criticalPath
    const criticalEdges = new Set(cp.slice(1).map((id, i) => `${cp[i]}->${id}`))
    return visible.flatMap((t) =>
      t.dependsOn
        .filter((d) => ids.has(d))
        .map((d) => {
          const id = `${d}->${t.id}`
          const critical = criticalEdges.has(id)
          const open = byId.get(d)?.status !== 'done'
          return {
            id,
            source: d,
            target: t.id,
            animated: critical,
            className: critical ? 'edge-critical' : open ? 'edge-open' : 'edge-done',
            markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
          }
        }),
    )
  }, [visible, tasks, analysis])

  const onNodesChange = useCallback((changes: NodeChange<TaskNodeType>[]) => {
    for (const c of changes) if (c.type === 'position' && c.position) pinned.current.set(c.id, c.position)
    // Tasks are deleted from the editor, never by a stray Backspace on the canvas.
    setNodes((ns) => applyNodeChanges(changes.filter((c) => c.type !== 'remove'), ns))
  }, [])

  const onConnect = useCallback(
    ({ source, target }: Connection) => {
      if (!source || !target) return
      if (wouldCreateCycle(tasks, target, source)) {
        toast('That dependency would create a cycle', 'warn')
        return
      }
      board.addDependency(target, source)
    },
    [board, tasks, toast],
  )

  return (
    <div className="graph">
      <div className="graph-toolbar">
        <label>
          <input type="checkbox" checked={hideDone} onChange={(e) => setHideDone(e.target.checked)} /> Hide done
        </label>
        <label>
          <input type="checkbox" checked={criticalOnly} onChange={(e) => setCriticalOnly(e.target.checked)} /> Critical path only
        </label>
        <button
          className="ghost"
          onClick={() => {
            pinned.current.clear()
            setNodes((ns) => ns.map((n) => ({ ...n, position: positions.get(n.id) ?? n.position })))
          }}
        >
          Re-layout
        </button>
        <span className="hint">Drag from a node's right handle to another node to add a dependency · select an edge and press Backspace to remove it</span>
      </div>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onConnect={onConnect}
        onEdgesDelete={(es) => es.forEach((e) => board.removeDependency(e.target, e.source))}
        deleteKeyCode={['Backspace', 'Delete']}
        onNodeClick={(_, n) => onOpen(n.id)}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        minZoom={0.2}
        proOptions={{ hideAttribution: true }}
        colorMode="system"
      >
        <Background gap={20} />
        <Controls showInteractive={false} />
        <MiniMap pannable zoomable nodeClassName={(n) => ((n.data as TaskNodeData).critical ? 'mm-critical' : 'mm-node')} />
      </ReactFlow>
    </div>
  )
}
