import type { EstimateQuality } from '../types'

/**
 * An open issue with no estimate counts as zero days, so it silently pulls the forecast in. The
 * more of the open plan that's unestimated, the less the ship date means. Mirrors server/quality.go.
 */
export const QUALITY_HIGH = 0.1
export const QUALITY_MEDIUM = 0.3

export const isUnestimated = (estimate: number) => !(estimate > 0)

export function estimateQuality(open: { estimate: number }[]): EstimateQuality {
  const unestimated = open.filter((i) => isUnestimated(i.estimate)).length
  const share = open.length ? unestimated / open.length : 0
  return {
    openIssues: open.length,
    unestimated,
    share,
    confidence: share < QUALITY_HIGH ? 'high' : share < QUALITY_MEDIUM ? 'medium' : 'low',
  }
}
