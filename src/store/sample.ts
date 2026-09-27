import type { Board } from './doc'
import type { Priority, Status } from '../lib/types'

type Seed = [key: string, title: string, status: Status, estimate: number, priority: Priority, assignee: string, deps: string[]]

const SEED: Seed[] = [
  ['spec', 'Write auth spec', 'done', 2, 'high', 'Priya', []],
  ['schema', 'Design database schema', 'done', 2, 'high', 'Marco', ['spec']],
  ['api', 'Build auth API', 'in_progress', 4, 'high', 'Marco', ['schema']],
  ['oauth', 'Integrate OAuth providers', 'todo', 3, 'medium', 'Lena', ['api']],
  ['ui', 'Login & signup screens', 'in_progress', 3, 'medium', 'Sam', ['spec']],
  ['e2e', 'End-to-end auth tests', 'backlog', 2, 'medium', 'Lena', ['oauth', 'ui']],
  ['audit', 'Security audit', 'backlog', 3, 'high', 'Priya', ['e2e']],
  ['docs', 'Developer docs', 'backlog', 1, 'low', 'Sam', ['api']],
  ['metrics', 'Auth metrics dashboard', 'todo', 2, 'low', '', ['api']],
  ['launch', 'Launch to beta users', 'backlog', 1, 'high', 'Priya', ['audit', 'docs']],
  ['ci', 'Speed up CI pipeline', 'review', 1, 'low', 'Marco', []],
  ['sentry', 'Set up error tracking', 'todo', 1, 'medium', 'Sam', []],
]

export function loadSample(board: Board) {
  board.doc.transact(() => {
    const ids = new Map<string, string>()
    SEED.forEach(([key, title, status, estimate, priority, assignee], i) => {
      ids.set(key, board.addTask({ title, status, estimate, priority, assignee, order: i }))
    })
    for (const [key, , , , , , deps] of SEED) for (const d of deps) board.addDependency(ids.get(key)!, ids.get(d)!)
  })
}
