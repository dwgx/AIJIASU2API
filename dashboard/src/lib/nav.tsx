import {
  ApcAccountsIcon,
  ApcBackupIcon,
  ApcDevicePoolIcon,
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
        to: '/operations',
        labelKey: 'nav.operations',
        icon: ApcRequestsIcon,
        keywords:
          'operations async switch select node durable queued running succeeded failed canceled 运维 操作 异步 切换 选择 节点 排队 运行 成功 失败 取消',
      },
      {
        to: '/leases',
        labelKey: 'nav.leases',
        icon: ApcLeasesIcon,
        keywords: 'sticky egress ip ttl rotate release 固定 出口 轮换 释放 租约',
      },
      {
        to: '/orders',
        labelKey: 'nav.orders',
        icon: ApcLeasesIcon,
        keywords:
          'orders plans settlement offline receipt payment refund entitlement activation 订单 套餐 结算 收款 退款 权益 开通',
      },
      {
        to: '/device-pool',
        labelKey: 'nav.devicePool',
        icon: ApcDevicePoolIcon,
        keywords:
          'device identity pool fingerprint seal generate assign retire quarantine purge export import 设备 身份 设备池 指纹 生成 绑定 退役 隔离 清除 导出 导入',
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
        to: '/tenants',
        labelKey: 'nav.tenants',
        icon: ApcUsersIcon,
        keywords:
          'tenants customers invitations onboarding activation portal 租户 客户 邀请 激活 开通 门户',
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
      {
        to: '/debug',
        labelKey: 'nav.debug',
        icon: ApcRequestsIcon,
        keywords:
          'debug trace ring stream switch gate session lease slot sequence in-memory redacted 调试 追踪 环形 流 切换 网关 会话 租约 槽位 序号 内存 脱敏',
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
      {
        to: '/backup',
        labelKey: 'nav.backup',
        icon: ApcBackupIcon,
        keywords:
          'backup restore export envelope ajsb passphrase audit 备份 恢复 导出 信封 口令 审计',
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
