import {
  ApcAccountsIcon,
  ApcLabIcon,
  ApcLeasesIcon,
  ApcLogsIcon,
  ApcNodesIcon,
  ApcOverviewIcon,
  ApcRequestsIcon,
  ApcSchedulesIcon,
  ApcSettingsIcon,
  ApcUsageIcon,
  ApcUsersIcon,
} from '../components/icons/ApcIcons'
import type { ApcIcon } from '../components/icons/ApcIcons'
import { SCHEDULES_GLYPH } from '../components/icons/morphGlyphs'
import type { MorphGlyph } from './iconMorph'
import type { TranslationKey } from './i18n'

export interface NavItem {
  to: string
  labelKey: TranslationKey
  icon: ApcIcon
  /** Optional morph glyph; the sidebar renders it instead of the static icon. */
  glyph?: MorphGlyph
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
        icon: ApcOverviewIcon,
        keywords: 'home dashboard metrics traffic health 首页 仪表盘 指标 流量 健康',
      },
      {
        to: '/usage',
        labelKey: 'nav.usage',
        icon: ApcUsageIcon,
        keywords:
          'usage requests bytes errors latency quota billing 用量 请求 流量 错误 延迟 配额 计费',
      },
      {
        to: '/accounts',
        labelKey: 'nav.accounts',
        icon: ApcAccountsIcon,
        keywords: 'vendor login credentials concurrency 厂商 登录 凭据 并发 账户',
      },
      {
        to: '/nodes',
        labelKey: 'nav.nodes',
        icon: ApcNodesIcon,
        keywords: 'servers regions latency capacity drain 服务器 区域 延迟 容量 排水 节点',
      },
      {
        to: '/leases',
        labelKey: 'nav.leases',
        icon: ApcLeasesIcon,
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
        icon: ApcUsersIcon,
        keywords: 'api keys allowlist quota platform 密钥 白名单 配额 平台 用户',
      },
      {
        to: '/requests',
        labelKey: 'nav.requests',
        icon: ApcRequestsIcon,
        keywords: 'logs traffic targets status latency mutation 请求 目标 状态 延迟 变更',
      },
      {
        to: '/logs',
        labelKey: 'nav.logs',
        icon: ApcLogsIcon,
        keywords: 'audit events severity actor action export 审计 事件 级别 操作 导出 日志',
      },
    ],
  },
  {
    labelKey: 'nav.system',
    items: [
      {
        to: '/schedules',
        labelKey: 'nav.schedules',
        icon: ApcSchedulesIcon,
        glyph: SCHEDULES_GLYPH,
        keywords:
          'schedules cron interval once rotate reconnect renewal preview runs history dry run 计划 任务 定时 轮换 重连 续费 预览 运行 历史 试运行',
      },
      {
        to: '/settings',
        labelKey: 'nav.settings',
        icon: ApcSettingsIcon,
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
        icon: ApcLabIcon,
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
