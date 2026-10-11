/// <reference lib="webworker" />
import { analyze, type GraphNode } from './graph'
import { simulateConfidence, spreadFrom, type SpreadTable } from './montecarlo'

/** Runs the full Monte Carlo simulation off the main thread for big plans (see useProject). */
export interface ConfidenceRequest {
  key: string
  nodes: GraphNode[]
  dueDates: [string, string | null][]
  types: Record<string, string>
  spreads: SpreadTable
}

self.onmessage = (e: MessageEvent<ConfidenceRequest>) => {
  const { key, nodes, dueDates, types, spreads } = e.data
  const confidence = simulateConfidence(nodes, analyze(nodes), new Map(dueDates), spreadFrom((id) => types[id], spreads))
  ;(self as unknown as DedicatedWorkerGlobalScope).postMessage({ key, confidence })
}
