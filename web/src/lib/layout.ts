import dagre from '@dagrejs/dagre'

export const NODE_W = 240
export const NODE_H = 100

export type Positions = Map<string, { x: number; y: number }>

/** Left-to-right layered layout of the dependency graph. Superlinear: big graphs go to a worker. */
export function layoutGraph(ids: string[], edges: [string, string][]): Positions {
  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'LR', nodesep: 24, ranksep: 64, marginx: 24, marginy: 24 })
  g.setDefaultEdgeLabel(() => ({}))
  for (const id of ids) g.setNode(id, { width: NODE_W, height: NODE_H })
  for (const [a, b] of edges) g.setEdge(a, b)
  dagre.layout(g)
  return new Map(
    ids.map((id) => {
      const n = g.node(id)
      return [id, { x: n.x - NODE_W / 2, y: n.y - NODE_H / 2 }]
    }),
  )
}
