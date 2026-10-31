import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from '@/app'
import type { Bootstrap } from '@/types'
import '@/styles.css'

const root = document.getElementById('root')
if (root?.dataset.bootstrap) {
  const data: Bootstrap = JSON.parse(root.dataset.bootstrap)
  createRoot(root).render(<StrictMode><App data={data} /></StrictMode>)
}
