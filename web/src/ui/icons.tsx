import {
  AlertTriangle,
  ArrowRight,
  Ban,
  BarChart3,
  Calendar,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  ChevronsUpDown,
  Clock,
  Copy,
  Eye,
  Filter,
  Flame,
  FolderKanban,
  GanttChart,
  Home,
  Info,
  Kanban,
  Link2,
  List,
  LogOut,
  Monitor,
  Moon,
  MoreHorizontal,
  Network,
  PanelLeft,
  Plus,
  Route,
  Search,
  Settings,
  Sparkles,
  Sun,
  Tag,
  Target,
  Trash2,
  User,
  UserPlus,
  Users,
  WifiOff,
  X,
  Zap,
  type LucideIcon,
} from 'lucide-react'
import type { IssueType, Priority } from '../types'

const ICONS = {
  'arrow-right': ArrowRight,
  block: Ban,
  board: Kanban,
  calendar: Calendar,
  check: Check,
  'check-circle': CheckCircle2,
  'chevron-down': ChevronDown,
  'chevron-right': ChevronRight,
  'chevrons': ChevronsUpDown,
  clock: Clock,
  copy: Copy,
  eye: Eye,
  filter: Filter,
  flame: Flame,
  graph: Network,
  home: Home,
  info: Info,
  insights: BarChart3,
  link: Link2,
  list: List,
  logout: LogOut,
  monitor: Monitor,
  moon: Moon,
  more: MoreHorizontal,
  panel: PanelLeft,
  plus: Plus,
  route: Route,
  projects: FolderKanban,
  search: Search,
  settings: Settings,
  sparkles: Sparkles,
  sun: Sun,
  tag: Tag,
  target: Target,
  timeline: GanttChart,
  trash: Trash2,
  user: User,
  'user-plus': UserPlus,
  users: Users,
  warning: AlertTriangle,
  offline: WifiOff,
  x: X,
  zap: Zap,
} satisfies Record<string, LucideIcon>

export type IconName = keyof typeof ICONS

export function Icon({ name, size = 16, className }: { name: IconName; size?: number; className?: string }) {
  const C = ICONS[name]
  return <C size={size} strokeWidth={2} className={className} aria-hidden />
}

/** Issue type glyphs: a soft tinted tile with a coloured mark, so types read at a glance without shouting. */
export function TypeIcon({ type, size = 16 }: { type: IssueType; size?: number }) {
  const c = `var(--type-${type})`
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" className="type-icon" aria-label={type} role="img">
      <rect width="16" height="16" rx="4.5" fill={c} fillOpacity=".16" />
      {type === 'story' && <path d="M5.2 3.6h5.6v8.8L8 10.4l-2.8 2z" fill={c} />}
      {type === 'task' && <path d="M4.6 8.3l2.2 2.2 4.6-4.9" stroke={c} strokeWidth="2" fill="none" strokeLinecap="round" strokeLinejoin="round" />}
      {type === 'bug' && <circle cx="8" cy="8" r="3.3" fill={c} />}
      {type === 'epic' && <path d="M9.2 2.8L5 8.9h3l-1.2 4.3L11 7.1H8z" fill={c} />}
    </svg>
  )
}

/** Workflow state as a ring that fills up: empty for to do, half for in progress, solid with a tick for done. */
export function StatusGlyph({ category, size = 14 }: { category: 'todo' | 'in_progress' | 'done'; size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" className={`status-glyph sg-${category}`} aria-hidden>
      <circle cx="8" cy="8" r="6.2" fill="none" stroke="currentColor" strokeWidth="1.8" strokeDasharray={category === 'todo' ? '2.4 1.9' : undefined} />
      {category === 'in_progress' && <path d="M8 3.6a4.4 4.4 0 0 1 0 8.8z" fill="currentColor" />}
      {category === 'done' && (
        <>
          <circle cx="8" cy="8" r="7" fill="currentColor" />
          <path d="M5.3 8.2l1.8 1.8 3.6-3.8" stroke="var(--surface)" strokeWidth="1.8" fill="none" strokeLinecap="round" strokeLinejoin="round" />
        </>
      )}
    </svg>
  )
}

export function PriorityIcon({ priority, size = 16 }: { priority: Priority; size?: number }) {
  const up = priority === 'high' || priority === 'highest'
  const double = priority === 'highest' || priority === 'lowest'
  const color = `var(--priority-${priority})`
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" className="priority-icon" aria-label={`${priority} priority`} role="img">
      {priority === 'medium' ? (
        <path d="M3 6h10M3 10h10" stroke={color} strokeWidth="2" strokeLinecap="round" />
      ) : (
        <g stroke={color} strokeWidth="2" fill="none" strokeLinecap="round" strokeLinejoin="round" transform={up ? undefined : 'rotate(180 8 8)'}>
          <path d={double ? 'M3.5 8.5L8 4l4.5 4.5' : 'M3.5 10L8 5.5l4.5 4.5'} />
          {double && <path d="M3.5 12.5L8 8l4.5 4.5" />}
        </g>
      )}
    </svg>
  )
}

export function Logo({ size = 24 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden className="logo">
      <rect width="32" height="32" rx="9" fill="var(--logo-bg)" />
      <path d="M9 21.5L16 10.5" stroke="var(--accent)" strokeWidth="2.4" />
      <path d="M16 10.5L23 21.5" stroke="var(--critical)" strokeWidth="2.4" />
      <circle cx="9" cy="21.5" r="3.4" fill="var(--accent)" />
      <circle cx="16" cy="10.5" r="3.4" fill="var(--logo-fg)" />
      <circle cx="23" cy="21.5" r="3.4" fill="var(--critical)" />
    </svg>
  )
}
