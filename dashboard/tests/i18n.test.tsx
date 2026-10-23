import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import { dictionaries, translate } from '../src/lib/i18n'

function openPalette() {
  act(() => {
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true }))
  })
}

beforeEach(async () => {
  localStorage.clear()
  document.documentElement.lang = ''
  window.history.pushState({}, '', '/')
  await api.resetMock()
})

describe('i18n dictionaries', () => {
  it('mirrors every English key in Chinese and never ships an empty string', () => {
    const enKeys = Object.keys(dictionaries.en).sort()
    const zhKeys = Object.keys(dictionaries.zh).sort()
    expect(zhKeys).toEqual(enKeys)
    for (const key of enKeys) {
      expect(dictionaries.en[key].trim(), `en.${key}`).not.toBe('')
      expect(dictionaries.zh[key].trim(), `zh.${key}`).not.toBe('')
    }
  })

  it('interpolates parameters and falls back to English then the key', () => {
    expect(translate('zh', 'accounts.selected', { count: 3 })).toBe('已选择 3 个')
    expect(translate('zh', 'missing.key')).toBe('missing.key')
    expect(translate('en', 'accounts.selected', { count: 2 })).toBe('2 selected')
  })
})

describe('language switching', () => {
  it('switches the sidebar and Lab to Chinese and updates document.lang', async () => {
    const user = userEvent.setup()
    render(<App />)

    expect(await screen.findByRole('heading', { name: 'Overview', level: 1 })).toBeInTheDocument()
    expect(document.documentElement.lang).toBe('en')

    const sidebar = screen.getByRole('complementary', { name: 'Primary navigation' })
    await user.click(within(sidebar).getByRole('button', { name: '中' }))

    expect(within(sidebar).getByRole('link', { name: '概览' })).toBeInTheDocument()
    expect(within(sidebar).getByRole('link', { name: '实验室' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '概览', level: 1 })).toBeInTheDocument()
    await waitFor(() => expect(document.documentElement.lang).toBe('zh-CN'))
    expect(localStorage.getItem('ajs.lang')).toBe('zh')

    await user.click(within(sidebar).getByRole('link', { name: '实验室' }))
    expect(await screen.findByRole('heading', { name: '实验室', level: 1 })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '概览' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('tab', { name: '网关与会话' })).toBeInTheDocument()
  })

  it('restores a stored Chinese choice on a fresh mount', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    window.history.pushState({}, '', '/lab')
    render(<App />)

    expect(await screen.findByRole('heading', { name: '实验室', level: 1 })).toBeInTheDocument()
    await waitFor(() => expect(document.documentElement.lang).toBe('zh-CN'))
    expect(screen.getByRole('complementary', { name: '主导航' })).toBeInTheDocument()
  })

  it('defaults to Chinese when the browser language starts with zh', async () => {
    vi.spyOn(window.navigator, 'language', 'get').mockReturnValue('zh-CN')
    render(<App />)

    expect(await screen.findByRole('heading', { name: '概览', level: 1 })).toBeInTheDocument()
    await waitFor(() => expect(localStorage.getItem('ajs.lang')).toBe('zh'))
  })

  it('switches back to English from the Settings language card', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    window.history.pushState({}, '', '/settings')
    render(<App />)

    expect(await screen.findByRole('heading', { name: '设置', level: 1 })).toBeInTheDocument()
    const group = await within(screen.getByRole('main')).findByRole('group', { name: '语言' })
    await userEvent.setup().click(within(group).getByRole('button', { name: 'English' }))

    expect(await screen.findByRole('heading', { name: 'Settings', level: 1 })).toBeInTheDocument()
    await waitFor(() => expect(document.documentElement.lang).toBe('en'))
    expect(localStorage.getItem('ajs.lang')).toBe('en')
    expect(screen.getByRole('complementary', { name: 'Primary navigation' })).toBeInTheDocument()
  })

  it('finds a page with a Chinese query while the UI is English', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'Overview', level: 1 })

    openPalette()
    const dialog = await screen.findByRole('dialog', { name: 'Command palette' })
    await user.type(within(dialog).getByRole('textbox', { name: 'Command palette search' }), '日志')

    expect(within(dialog).getByRole('option', { name: /Logs/ })).toBeInTheDocument()
  })

  it('finds a page with an English query while the UI is Chinese', async () => {
    const user = userEvent.setup()
    localStorage.setItem('ajs.lang', 'zh')
    render(<App />)
    await screen.findByRole('heading', { name: '概览', level: 1 })

    openPalette()
    const dialog = await screen.findByRole('dialog', { name: '命令面板' })
    await user.type(within(dialog).getByRole('textbox', { name: '命令面板搜索' }), 'leas')

    expect(within(dialog).getByRole('option', { name: /租约/ })).toBeInTheDocument()
  })
})
