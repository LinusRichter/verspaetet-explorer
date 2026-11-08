import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { AppProvider, useApp } from './state'
import type { Tab } from './types'
import { ScrubBar } from './components/ScrubBar'
import { Breadcrumbs } from './components/Breadcrumbs'
import { Board } from './components/Board'
import { Journey } from './components/Journey'
import { LineView } from './components/LineView'
import { DayView } from './components/DayView'
import { Palette } from './components/Palette'
import { ErrorBox } from './components/bits'
import { isTypingTarget } from './lib'

const TABS: { id: Tab; label: string }[] = [
  { id: 'board', label: 'Bahnhofstafel' },
  { id: 'journey', label: 'Zuglauf' },
  { id: 'line', label: 'Linie' },
  { id: 'day', label: 'Übersicht' },
]

function Shell(): ReactNode {
  const { metaError, nav, setTab, theme, toggleTheme } = useApp()
  const [palOpen, setPalOpen] = useState(false)
  const closePal = useCallback(() => setPalOpen(false), [])

  useEffect(() => {
    document.documentElement.dataset.theme = theme
  }, [theme])

  // Ctrl+K / Cmd+K opens the palette everywhere; "/" (QWERTZ: Shift+7) only
  // outside input fields — there "/" must stay typeable
  // ("Köln Messe/Deutz", "Siegburg/Bonn").
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPalOpen(true)
      } else if (e.key === '/' && !e.ctrlKey && !e.metaKey && !e.altKey && !isTypingTarget(e.target)) {
        e.preventDefault()
        setPalOpen(true)
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [])

  return (
    <div className="app">
      <header>
        <div className="hdr-row">
          <div className="brand">verspaetet</div>
          <nav className="tabs">
            {TABS.map((t) => (
              <button key={t.id} className={`tab${nav.tab === t.id ? ' active' : ''}`} onClick={() => setTab(t.id)}>
                {t.label}
              </button>
            ))}
          </nav>
          <div className="hdr-right">
            <button className="mini" onClick={() => setPalOpen(true)} title="Suche (Ctrl+K oder /)">
              ⌕ Suche
            </button>
            <button className="mini" onClick={toggleTheme}>{theme === 'dark' ? '☀ hell' : '🌙 dunkel'}</button>
          </div>
        </div>
        {metaError ? <div className="scrub-row"><ErrorBox msg={`Meta: ${metaError}`} /></div> : <ScrubBar />}
      </header>
      <Breadcrumbs />
      <main>
        {nav.tab === 'board' && <Board />}
        {nav.tab === 'journey' && <Journey />}
        {nav.tab === 'line' && <LineView />}
        {nav.tab === 'day' && <DayView />}
      </main>
      <footer>
        Daten: Deutsche Bahn (Timetables API &amp; StaDa API), CC BY 4.0 · Datensatz:{' '}
        <a href="https://huggingface.co/datasets/LinusRichter404/verspaetet">verspaetet</a> · Suche: Ctrl+K oder /
      </footer>
      <Palette open={palOpen} onClose={closePal} />
    </div>
  )
}

export default function App(): ReactNode {
  return (
    <AppProvider>
      <Shell />
    </AppProvider>
  )
}