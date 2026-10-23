import {
  Activity,
  FlaskConical,
  KeyRound,
  LayoutDashboard,
  ScrollText,
  Server,
  Settings,
  Ticket,
  Users,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

export interface NavItem {
  to: string
  label: string
  icon: LucideIcon
  keywords: string
}

export interface NavSection {
  label: string
  items: NavItem[]
}

export const NAV_SECTIONS: NavSection[] = [
  {
    label: 'Operate',
    items: [
      { to: '/', label: 'Overview', icon: LayoutDashboard, keywords: 'home dashboard metrics traffic health' },
      { to: '/accounts', label: 'Accounts', icon: KeyRound, keywords: 'vendor login credentials concurrency' },
      { to: '/nodes', label: 'Nodes', icon: Server, keywords: 'servers regions latency capacity drain' },
      { to: '/leases', label: 'Leases', icon: Ticket, keywords: 'sticky egress ip ttl rotate release' },
    ],
  },
  {
    label: 'Access',
    items: [
      { to: '/users', label: 'Users', icon: Users, keywords: 'api keys allowlist quota platform' },
      { to: '/requests', label: 'Requests', icon: Activity, keywords: 'logs traffic targets status latency mutation' },
      { to: '/logs', label: 'Logs', icon: ScrollText, keywords: 'audit events severity actor action export' },
    ],
  },
  {
    label: 'System',
    items: [{ to: '/settings', label: 'Settings', icon: Settings, keywords: 'transport health lease subscription theme reset' }],
  },
  {
    label: 'Lab',
    items: [
      {
        to: '/lab',
        label: 'Lab',
        icon: FlaskConical,
        keywords: 'lab telemetry gates sessions servers protocol device logs diagnostics flags redaction',
      },
    ],
  },
]

export const NAV_ITEMS: NavItem[] = NAV_SECTIONS.flatMap((section) => section.items)
