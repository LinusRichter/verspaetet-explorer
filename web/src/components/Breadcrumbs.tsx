// Scope bar: station › line › run. Always shows where you are (also in the
// overview); every segment is clickable; "←" restores the last navigation
// state including the knowledge time.
// No own fetch: the line label/context comes from the data the active view
// (line/trip) has loaded anyway (state.viewLine).

import type { ReactNode } from 'react'
import { useApp } from '../state'
import { fmt } from '../lib'

export function Breadcrumbs(): ReactNode {
  const { nav, viewLine, canGoBack, goStation, goLine, back } = useApp()

  const line =
    nav.tab === 'line' ? (viewLine && viewLine.key === nav.line ? viewLine : null)
      : nav.tab === 'journey' ? (viewLine && viewLine.run === nav.run ? viewLine : null)
        : null

  const parts: ReactNode[] = []
  if (nav.station) {
    parts.push(
      nav.tab === 'board'
        ? <span key="st" className="crumb cur">{nav.station}</span>
        : <span key="st" className="crumb" onClick={() => goStation(nav.station!)}>{nav.station}</span>,
    )
  }
  if (line) {
    parts.push(
      nav.tab === 'line'
        ? <span key="li" className="crumb cur">{line.label}{line.ctx ? ` (${line.ctx})` : ''}</span>
        : <span key="li" className="crumb" onClick={() => goLine(line.key, nav.station)}>{line.label}</span>,
    )
  }
  if (nav.tab === 'journey' && nav.run) {
    const dep = nav.runDep ?? (line?.dep ?? null)
    parts.push(<span key="ru" className="crumb cur">Fahrt{dep != null ? ` ${fmt(dep)}` : ''}</span>)
  }

  return (
    <div className="crumbbar">
      <button className="mini" onClick={back} disabled={!canGoBack} title="zurück">←</button>
      {parts.length === 0
        ? <span className="muted">kein Kontext — Bahnhof oder Linie suchen (Ctrl+K)</span>
        : parts.reduce<ReactNode[]>((acc, p, i) =>
          i === 0 ? [p] : [...acc, <span key={`sep${i}`} className="crumb-sep">›</span>, p], [])}
    </div>
  )
}
