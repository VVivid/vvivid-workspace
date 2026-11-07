import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserApp } from '@/browser-app'
import type { Bootstrap } from '@/types'
import '@/styles.css'

const root = document.getElementById('root')
if (root?.dataset.bootstrap) {
  const data: Bootstrap = JSON.parse(root.dataset.bootstrap)
  root.removeAttribute('data-bootstrap')
  createRoot(root).render(<StrictMode><BrowserApp initialData={data} /></StrictMode>)
}
