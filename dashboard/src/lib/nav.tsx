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
import type { TranslationKey } from './i18n'

export interface NavItem {
  to: string
  labelKey: TranslationKey
  icon: LucideIcon
  /** Search terms in both languages so the command palette matches either. */
  keywords: string
}

export interface NavSection {
  labelKey: TranslationKey
  items: NavItem[]
}

export const NAV_SECTIONS: NavSection[] = [
  {
    labelKey: 'nav.operate',
    items: [
      {
        to: '/',
        labelKey: 'nav.overview',
        icon: LayoutDashboard,
        keywords: 'home dashboard metrics traffic health 首页 仪表盘 指标 流量 健康',
      },
      {
        to: '/accounts',
        labelKey: 'nav.accounts',
        icon: KeyRound,
        keywords: 'vendor login credentials concurrency 厂商 登录 凭据 并发 账户',
      },
      {
        to: '/nodes',
        labelKey: 'nav.nodes',
        icon: Server,
        keywords: 'servers regions latency capacity drain 服务器 区域 延迟 容量 排水 节点',
      },
      {
        to: '/leases',
        labelKey: 'nav.leases',
        icon: Ticket,
        keywords: 'sticky egress ip ttl rotate release 固定 出口 轮换 释放 租约',
      },
    ],
  },
  {
    labelKey: 'nav.access',
    items: [
      {
        to: '/users',
        labelKey: 'nav.users',
        icon: Users,
        keywords: 'api keys allowlist quota platform 密钥 白名单 配额 平台 用户',
      },
      {
        to: '/requests',
        labelKey: 'nav.requests',
        icon: Activity,
        keywords: 'logs traffic targets status latency mutation 请求 目标 状态 延迟 变更',
      },
      {
        to: '/logs',
        labelKey: 'nav.logs',
        icon: ScrollText,
        keywords: 'audit events severity actor action export 审计 事件 级别 操作 导出 日志',
      },
    ],
  },
  {
    labelKey: 'nav.system',
    items: [
      {
        to: '/settings',
        labelKey: 'nav.settings',
        icon: Settings,
        keywords: 'transport health lease subscription theme language reset 传输 健康 租约 订阅 主题 语言 重置 设置',
      },
    ],
  },
  {
    labelKey: 'nav.labSection',
    items: [
      {
        to: '/lab',
        labelKey: 'nav.lab',
        icon: FlaskConical,
        keywords:
          'lab telemetry gates sessions servers protocol device logs diagnostics flags redaction 实验室 遥测 网关 会话 服务器 协议 设备 日志 诊断 开关 脱敏',
      },
    ],
  },
]

export const NAV_ITEMS: NavItem[] = NAV_SECTIONS.flatMap((section) => section.items)

export function navItemFor(pathname: string): NavItem | undefined {
  return NAV_ITEMS.find((item) => item.to === pathname)
}
