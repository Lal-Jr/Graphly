import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { customAlphabet } from 'nanoid'
import '@xyflow/react/dist/style.css'
import './styles.css'
import App from './App'
import { Board } from './store/doc'
import { BoardProvider } from './store/context'

function currentRoom(): string {
  const match = location.hash.match(/room=([\w-]+)/)
  if (match) return match[1]
  const room = customAlphabet('abcdefghijkmnpqrstuvwxyz23456789', 10)()
  history.replaceState(null, '', `#room=${room}`)
  return room
}

const board = new Board(currentRoom())
// Switching rooms means a new Yjs doc and providers — simplest to start fresh.
window.addEventListener('hashchange', () => location.reload())

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BoardProvider board={board}>
      <App />
    </BoardProvider>
  </StrictMode>,
)
