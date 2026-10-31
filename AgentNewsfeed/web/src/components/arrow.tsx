export function Arrow({ direction = 'right' }: { direction?: 'right' | 'left' | 'up-right' }) {
  const path = direction === 'left' ? 'M19 12H5m7-7-7 7 7 7' : direction === 'up-right' ? 'M7 17 17 7M7 7h10v10' : 'M5 12h14m-7-7 7 7-7 7'
  return <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d={path} stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" /></svg>
}
