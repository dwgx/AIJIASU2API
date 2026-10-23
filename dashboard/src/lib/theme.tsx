import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import type { ThemeName } from './types'

export type AccentName = 'mint' | 'indigo' | 'violet' | 'orange'

const ACCENT_NAMES: readonly AccentName[] = ['mint', 'indigo', 'violet', 'orange']

interface ThemeContextValue {
  theme: ThemeName
  setTheme: (theme: ThemeName) => void
  toggle: () => void
  accent: AccentName
  setAccent: (accent: AccentName) => void
}

const ThemeContext = createContext<ThemeContextValue | null>(null)

function initialTheme(): ThemeName {
  if (typeof localStorage === 'undefined') return 'dark'
  const stored = localStorage.getItem('ajs.theme')
  return stored === 'light' ? 'light' : 'dark'
}

function isAccent(value: string | null): value is AccentName {
  return value !== null && (ACCENT_NAMES as readonly string[]).includes(value)
}

function initialAccent(): AccentName {
  if (typeof localStorage === 'undefined') return 'mint'
  const stored = localStorage.getItem('ajs.accent')
  return isAccent(stored) ? stored : 'mint'
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<ThemeName>(initialTheme)
  const [accent, setAccentState] = useState<AccentName>(initialAccent)

  useEffect(() => {
    document.documentElement.dataset.theme = theme
    document.documentElement.style.colorScheme = theme
    document.documentElement.dataset.accent = accent
    localStorage.setItem('ajs.theme', theme)
    localStorage.setItem('ajs.accent', accent)
  }, [theme, accent])

  const setTheme = useCallback((next: ThemeName) => setThemeState(next), [])
  const toggle = useCallback(() => setThemeState((current) => (current === 'dark' ? 'light' : 'dark')), [])
  const setAccent = useCallback((next: AccentName) => setAccentState(next), [])

  const value = useMemo(
    () => ({ theme, setTheme, toggle, accent, setAccent }),
    [theme, setTheme, toggle, accent, setAccent],
  )
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}

export function useTheme(): ThemeContextValue {
  const context = useContext(ThemeContext)
  if (!context) throw new Error('useTheme must be used inside ThemeProvider')
  return context
}
