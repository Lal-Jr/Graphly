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
  Plus,
  Search,
  Settings,
  Sparkles,
  Sun,
  Tag,
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
  plus: Plus,
  projects: FolderKanban,
  search: Search,
  settings: Settings,
  sparkles: Sparkles,
  sun: Sun,
  tag: Tag,
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

/** Jira-style issue type glyphs: a coloured tile with a white mark. */
export function TypeIcon({ type, size = 16 }: { type: IssueType; size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" className="type-icon" aria-label={type} role="img">
      <rect width="16" height="16" rx="3" fill={`var(--type-${type})`} />
      {type === 'story' && <path d="M5 3.5h6v9L8 10.3 5 12.5z" fill="#fff" />}
      {type === 'task' && <path d="M4.5 8.2l2.3 2.3 4.7-5" stroke="#fff" strokeWidth="1.9" fill="none" strokeLinecap="round" strokeLinejoin="round" />}
      {type === 'bug' && <circle cx="8" cy="8" r="3.4" fill="#fff" />}
      {type === 'epic' && <path d="M9.2 2.8L5 8.9h3l-1.2 4.3L11 7.1H8z" fill="#fff" />}
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
      <rect width="32" height="32" rx="8" fill="var(--brand)" />
      <path d="M11 11.5l10 4.5M11 20.5l10-4.5" stroke="#fff" strokeOpacity=".7" strokeWidth="2" />
      <circle cx="10" cy="11" r="3.5" fill="#fff" />
      <circle cx="22" cy="16" r="3.5" fill="#fff" />
      <circle cx="10" cy="21" r="3.5" fill="#fff" />
    </svg>
  )
}
