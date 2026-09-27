import { useCallback } from 'react'
import { useSearchParams } from 'react-router-dom'

/** The open issue lives in the URL (?issue=KEY-12), so every issue view is linkable. */
export function useIssueParam() {
  const [params, setParams] = useSearchParams()
  const open = useCallback(
    (key: string | null) =>
      setParams(
        (p) => {
          const next = new URLSearchParams(p)
          if (key) next.set('issue', key)
          else next.delete('issue')
          return next
        },
        { replace: !key },
      ),
    [setParams],
  )
  return [params.get('issue'), open] as const
}
