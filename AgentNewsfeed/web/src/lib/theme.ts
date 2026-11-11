export type Theme = 'light' | 'dark'
export const themeStorageKey = 'agentnewsfeed.appearance'

function systemTheme(): Theme {
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export function savedTheme(): Theme {
  try {
    const saved = window.localStorage.getItem(themeStorageKey)
    if (saved === 'light' || saved === 'dark') return saved
  } catch {
    // Restricted storage still permits changing appearance for this visit.
  }
  return systemTheme()
}

export function initializeTheme() {
  document.documentElement.dataset.theme = savedTheme()
}

