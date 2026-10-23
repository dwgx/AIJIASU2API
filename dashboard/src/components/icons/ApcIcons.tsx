import type { ComponentType, ReactNode } from 'react'
import type { Status } from '../../lib/types'

/**
 * APC glyphs — drawn inline on a 24px grid with `currentColor` so a single
 * component follows nav, hover, active and status tints in both themes.
 * Every glyph is decorative: the surrounding control carries the label.
 */
export interface ApcIconProps {
  size?: number
  className?: string
  'aria-hidden'?: boolean | 'true' | 'false'
}

export type ApcIcon = ComponentType<ApcIconProps>

function createIcon(name: string, paths: ReactNode, strokeWidth = 1.6): ApcIcon {
  function Icon({ size = 18, className, 'aria-hidden': ariaHidden = true }: ApcIconProps) {
    return (
      <svg
        width={size}
        height={size}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeLinejoin="round"
        className={className}
        aria-hidden={ariaHidden}
        focusable="false"
      >
        {paths}
      </svg>
    )
  }
  Icon.displayName = name
  return Icon
}

/* Brand ------------------------------------------------------------------ */

/** Geometric "A" gate with a network node at the apex. */
export const ApcBrandMark = createIcon(
  'ApcBrandMark',
  <>
    <path d="M4 19 12 5l8 14" />
    <path d="M8.4 13h7.2" />
    <circle cx="12" cy="5" r="1.6" fill="currentColor" stroke="none" />
  </>,
  1.8,
)

/* Navigation ------------------------------------------------------------- */

export const ApcOverviewIcon = createIcon(
  'ApcOverviewIcon',
  <>
    <rect x="3.5" y="3.5" width="7" height="7" rx="2" />
    <rect x="13.5" y="3.5" width="7" height="7" rx="2" />
    <rect x="3.5" y="13.5" width="7" height="7" rx="2" />
    <rect x="13.5" y="13.5" width="7" height="7" rx="2" />
  </>,
)

export const ApcAccountsIcon = createIcon(
  'ApcAccountsIcon',
  <>
    <circle cx="8" cy="12" r="3.5" />
    <path d="M11.5 12H21" />
    <path d="M18 12v3" />
    <path d="M15.5 12v2.5" />
  </>,
)

export const ApcNodesIcon = createIcon(
  'ApcNodesIcon',
  <>
    <rect x="3.5" y="4" width="17" height="6.5" rx="2" />
    <rect x="3.5" y="13.5" width="17" height="6.5" rx="2" />
    <path d="M7 7.25h.01M7 16.75h.01" strokeWidth="2" />
  </>,
)

export const ApcLeasesIcon = createIcon(
  'ApcLeasesIcon',
  <>
    <path d="M4 8.5A1.5 1.5 0 0 1 5.5 7h13A1.5 1.5 0 0 1 20 8.5v1.7a1.8 1.8 0 0 0 0 3.6v1.7a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 15.5v-1.7a1.8 1.8 0 0 0 0-3.6z" />
    <path d="M14.5 7.4v9.2" strokeDasharray="1.5 2.6" />
  </>,
)

export const ApcUsersIcon = createIcon(
  'ApcUsersIcon',
  <>
    <circle cx="9" cy="8.5" r="3" />
    <path d="M3.5 19c0-3 2.5-5 5.5-5s5.5 2 5.5 5" />
    <path d="M15.5 6.2a3 3 0 0 1 0 5.6" />
    <path d="M17 14.4c2 .6 3.5 2.3 3.5 4.6" />
  </>,
)

export const ApcRequestsIcon = createIcon(
  'ApcRequestsIcon',
  <>
    <path d="M4 8h13l-3-3" />
    <path d="M20 16H7l3 3" />
  </>,
)

export const ApcLogsIcon = createIcon(
  'ApcLogsIcon',
  <>
    <rect x="4.5" y="3.5" width="15" height="17" rx="2.5" />
    <path d="M8.5 8h7M8.5 12h7M8.5 16h4" />
  </>,
)

export const ApcSettingsIcon = createIcon(
  'ApcSettingsIcon',
  <>
    <path d="M4 7h10M18 7h2M4 17h4M12 17h8" />
    <circle cx="16" cy="7" r="2" />
    <circle cx="10" cy="17" r="2" />
  </>,
)

export const ApcLabIcon = createIcon(
  'ApcLabIcon',
  <>
    <path d="M9.5 3.5h5" />
    <path d="M10.5 3.5v6L5.2 18a2 2 0 0 0 1.7 3h10.2a2 2 0 0 0 1.7-3l-5.3-8.5v-6" />
    <path d="M7.6 15h8.8" />
  </>,
)

/* Shell utilities -------------------------------------------------------- */

export const ApcMenuIcon = createIcon('ApcMenuIcon', <path d="M4 7h16M4 12h16M4 17h16" />)

export const ApcPanelCollapseIcon = createIcon(
  'ApcPanelCollapseIcon',
  <>
    <rect x="3.5" y="4" width="17" height="16" rx="2.5" />
    <path d="M9.5 4v16" />
    <path d="M16 10l-2 2 2 2" />
  </>,
)

export const ApcPanelExpandIcon = createIcon(
  'ApcPanelExpandIcon',
  <>
    <rect x="3.5" y="4" width="17" height="16" rx="2.5" />
    <path d="M9.5 4v16" />
    <path d="M14 10l2 2-2 2" />
  </>,
)

export const ApcSearchIcon = createIcon(
  'ApcSearchIcon',
  <>
    <circle cx="11" cy="11" r="6.5" />
    <path d="M16 16l4 4" />
  </>,
)

export const ApcPlusIcon = createIcon('ApcPlusIcon', <path d="M12 5v14M5 12h14" />)

export const ApcRefreshIcon = createIcon(
  'ApcRefreshIcon',
  <>
    <path d="M4 12a8 8 0 0 1 13.7-5.6L20 8.5" />
    <path d="M20 4v4.5h-4.5" />
    <path d="M20 12a8 8 0 0 1-13.7 5.6L4 15.5" />
    <path d="M4 20v-4.5h4.5" />
  </>,
)

export const ApcSunIcon = createIcon(
  'ApcSunIcon',
  <>
    <circle cx="12" cy="12" r="4" />
    <path d="M12 2.5v2M12 19.5v2M4.6 4.6 6 6M18 18l1.4 1.4M2.5 12h2M19.5 12h2M4.6 19.4 6 18M18 6l1.4-1.4" />
  </>,
)

export const ApcMoonIcon = createIcon(
  'ApcMoonIcon',
  <path d="M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5z" />,
)

export const ApcCloseIcon = createIcon('ApcCloseIcon', <path d="M6 6l12 12M18 6 6 18" />)

export const ApcCheckIcon = createIcon('ApcCheckIcon', <path d="M5 12.5l4.5 4.5L19 7.5" />, 2.4)

export const ApcInboxIcon = createIcon(
  'ApcInboxIcon',
  <>
    <path d="M4 13.5 6.2 5.4A2 2 0 0 1 8.1 4h7.8a2 2 0 0 1 1.9 1.4L20 13.5" />
    <path d="M4 13.5h4.2l1.3 2.2h5l1.3-2.2H20v3.6a2.4 2.4 0 0 1-2.4 2.4H6.4A2.4 2.4 0 0 1 4 17.1z" />
  </>,
)

export const ApcSpinnerIcon = createIcon('ApcSpinnerIcon', <path d="M12 3.5a8.5 8.5 0 1 0 8.5 8.5" />, 2)

/* Status ----------------------------------------------------------------- */

const STATUS_PATHS: Record<Status, ReactNode> = {
  ok: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M8 12.2l2.6 2.6L16 9.4" />
    </>
  ),
  info: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 11v5" />
      <path d="M12 8h.01" strokeWidth="2" />
    </>
  ),
  warn: (
    <>
      <path d="M12 4.2 21 19.5H3z" />
      <path d="M12 10v4" />
      <path d="M12 17h.01" strokeWidth="2" />
    </>
  ),
  error: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M9.2 9.2l5.6 5.6M14.8 9.2l-5.6 5.6" />
    </>
  ),
  unavailable: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M6.5 6.5l11 11" />
    </>
  ),
}

/** Glyph paired with a status pill; colour follows the pill's status hue. */
export function ApcStatusGlyph({
  status,
  size = 12,
  className,
}: {
  status: Status
  size?: number
  className?: string
}) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      aria-hidden="true"
      focusable="false"
    >
      {STATUS_PATHS[status]}
    </svg>
  )
}
