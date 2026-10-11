/// <reference lib="webworker" />
import { layoutGraph } from './layout'

self.onmessage = (e: MessageEvent<{ key: string; ids: string[]; edges: [string, string][] }>) => {
  const { key, ids, edges } = e.data
  ;(self as unknown as DedicatedWorkerGlobalScope).postMessage({ key, positions: layoutGraph(ids, edges) })
}
