import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ThemePicker } from '@/theme'
import { initializeTheme, themeStorageKey } from '@/lib/theme'

beforeEach(() => {
  window.localStorage.clear()
  delete document.documentElement.dataset.theme
})
afterEach(() => {
  window.localStorage.clear()
  delete document.documentElement.dataset.theme
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('appearance preference', () => {
  it('uses the system initially and remembers an explicit choice on a later visit', async () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true }))
    initializeTheme()
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
    const user = userEvent.setup()
    const { unmount } = render(<ThemePicker />)
    expect(screen.getByRole('button', { name: 'Dark', pressed: true })).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Light' }))
    expect(window.localStorage.getItem(themeStorageKey)).toBe('light')
    unmount()
    initializeTheme()
    render(<ThemePicker />)
    expect(document.documentElement).toHaveAttribute('data-theme', 'light')
    expect(screen.getByRole('button', { name: 'Light', pressed: true })).toBeVisible()
  })

  it('responds to preference changes in another tab and keeps working if storage is unavailable', async () => {
    const user = userEvent.setup()
    render(<ThemePicker />)
    window.localStorage.setItem(themeStorageKey, 'dark')
    fireEvent(window, new StorageEvent('storage', { key: themeStorageKey, newValue: 'dark' }))
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new DOMException('Storage unavailable') })
    await user.click(screen.getByRole('button', { name: 'Light' }))
    expect(document.documentElement).toHaveAttribute('data-theme', 'light')
  })
})
