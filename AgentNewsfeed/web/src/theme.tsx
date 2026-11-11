import { useEffect, useLayoutEffect, useState } from 'react'

import { savedTheme, themeStorageKey } from '@/lib/theme'
import type { Theme } from '@/lib/theme'

export function ThemePicker() {
  const [theme, setTheme] = useState(savedTheme)
  useLayoutEffect(() => {
    document.documentElement.dataset.theme = theme
  }, [theme])
  useEffect(() => {
    function syncPreference(event: StorageEvent) {
      if (event.key === themeStorageKey || event.key === null) setTheme(savedTheme())
    }
    window.addEventListener('storage', syncPreference)
    return () => window.removeEventListener('storage', syncPreference)
  }, [])
  function chooseTheme(choice: Theme) {
    setTheme(choice)
    try {
      window.localStorage.setItem(themeStorageKey, choice)
    } catch {
      // Keep the working session choice when browser storage is unavailable.
    }
  }
  return <div className="theme-picker" role="group" aria-label="Appearance">
    {(['light', 'dark'] as const).map(choice => <button key={choice} type="button"
      aria-pressed={theme === choice} onClick={() => chooseTheme(choice)}>
      {choice === 'light' ? 'Light' : 'Dark'}
    </button>)}
  </div>
}
