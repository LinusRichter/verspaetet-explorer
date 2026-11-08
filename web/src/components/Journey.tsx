// Trip view (Zuglauf): stop strip, delay evolution, snapshot history.
// Reached only via navigation (board/line/search) — never preloaded.

import { useEffect, type ReactNode } from 'react'
import { useFetch } from '../useFetch'
import { api } from '../api'
import type { JourneyData } from '../types'
import { useApp } from '../state'
import { fmt, runStateAt, stateAt } from '../lib'
import { delayHex, palette } from '../theme'
import { Chip, Empty, ErrorBox, Loading } from './bits'
import { EvolutionChart } from './charts'

export function Journey(): ReactNode {
  const { nav, scrub, meta, theme, goStation, selectLineRun, selectRun, setScrub, setViewLine, setNavLine } = useApp()
  const p = palette(theme)
  const key = nav.run ? `${nav.run}|${nav.station ?? ''}|${nav.day ?? ''}` : null
  const { data, err, loading } = useFetch<JourneyData | null>(key, (s) => api.journey(nav.run!, nav.station, nav.day, s))

  // The breadcrumb gets line label + departure from THESE data (no extra fetch).
  useEffect(() => {
    if (!data) return
    setViewLine({ key: data.line.key, label: data.line.label, run: data.run.id, dep: data.run.dep })
    // Always adopt nav.line/URL to the line of THIS run — no matter through
    // which entry (overview, stale deep link) we came; otherwise the line
    // tab and a shared link would show a foreign line.
    setNavLine(data.line.key)
  }, [data, setViewLine, setNavLine])

  if (!nav.run) return <Empty hint="Zuglauf öffnen: Abfahrt in der Tafel oder Fahrt in der Linien-Ansicht anklicken." />
  if (err) return <ErrorBox msg={err} />
  if (!loading && !data) return <Empty hint="Fahrt nicht gefunden (an diesem Tag keine Daten zu dieser Fahrt)." />
  if (!data || !meta) return <Loading note="Zuglauf wird geladen …" />

  const { run, line } = data
  // Stop selection via the IRIS index (1-based, like board/search) — never
  // via array position: missing stops would otherwise highlight the wrong one.
  // Fallback: the stop at the context station, else the first.
  let selIdx = nav.stopIdx ? run.stops.findIndex((s) => s.idx === nav.stopIdx) : -1
  if (selIdx < 0 && nav.station) selIdx = run.stops.findIndex((s) => s.name === nav.station)
  if (selIdx < 0) selIdx = 0
  const selStop = run.stops[selIdx]
  const maxD = Math.max(15, ...run.stops.map((s) => s.finalD)) + 3

  return (
    <section>
      {data.altLines.length > 0 && (
        <div className="chipsline">
          <span className="muted chipslabel">{data.altScoped ? `Linien ab ${nav.station}:` : 'Linien:'}</span>
          {data.altLines.map((l) => (
            <button key={l.key} className={`lchip${l.key === line.key ? ' sel' : ''}`}
              aria-pressed={l.key === line.key}
              onClick={() => selectLineRun(l.key, l.runId, l.dep)}>
              {l.label}
            </button>
          ))}
        </div>
      )}
      <div className="runhead">
        <b>{line.label}</b>{line.evu ? <> · {line.evu}</> : null} · Start {fmt(run.dep)}{' '}
        <span className="stname-link" onClick={() => goStation(run.stops[0].name)}>{run.stops[0].name}</span>
        {' → '}
        <span className="stname-link" onClick={() => goStation(run.stops[run.stops.length - 1].name)}>
          {run.stops[run.stops.length - 1].name}
        </span>
        {' · '}{run.stops.length} Halte · <span className="state">{runStateAt(run, scrub)}</span>
      </div>

      <div className="stops">
        {run.stops.map((st, i) => {
          const sta = stateAt(st, scrub)
          const d = sta.known && sta.delay != null ? sta.delay : 0
          return (
            <div key={st.idx || i} className={`stop${i === selIdx ? ' sel' : ''}`} onClick={() => selectRun(nav.run!, st.idx, nav.runDep ?? undefined)}>
              <div className="st-name" title={st.name}
                onClick={(e) => { e.stopPropagation(); goStation(st.name) }}>{st.name}</div>
              <div className="st-time">{fmt(st.planned)}</div>
              <Chip st={sta} compact />
              <div className="st-plat">
                Gl. {(sta.known ? sta.plat : '') || st.platFrom || '–'}
                {st.platTo !== st.platFrom && sta.known && sta.plat === st.platTo && <span className="pchg">⇄</span>}
              </div>
              <div className="st-bar">
                <i style={{
                  width: `${Math.min(100, Math.abs(d) / (maxD * 0.7) * 100)}%`,
                  background: sta.cancelled ? p.bad : delayHex(p, d),
                  float: d < 0 ? 'right' : undefined,
                }} />
              </div>
            </div>
          )
        })}
      </div>

      <div className="grid2">
        <div className="panel">
          <h3>Verspätungs-Entwicklung — X = Wissensstand, Y = kommunizierte Verspätung (min), 1 Kurve = 1 Halt · Punkt = Snapshot</h3>
          <EvolutionChart run={run} selIdx={selIdx} meta={meta} scrub={scrub} onScrub={setScrub}
            onSelectStop={(i) => selectRun(nav.run!, run.stops[i].idx, nav.runDep ?? undefined)} p={p} />
        </div>
        <div className="panel">
          <h3>Snapshot-Historie „{selStop.name}“ · {selStop.snaps.length} Snapshot{selStop.snaps.length === 1 ? '' : 's'}</h3>
          <div className="tblwrap"><table className="tbl">
            <thead>
              <tr><th className="t">Wissensstand</th><th>Status</th><th className="t">±</th><th className="t">Zeit</th><th className="t">Gl.</th></tr>
            </thead>
            <tbody>
              {selStop.snaps.map((sn, i) => (
                <tr key={i} style={sn.at <= scrub ? undefined : { opacity: 0.35 }}>
                  <td className="t">{fmt(sn.at)}</td>
                  <td>{sn.ist ? 'Ist' : sn.cancelled ? 'Entfall' : 'Prognose'}</td>
                  <td className="t">{sn.cancelled || sn.prog == null ? '–' : `${sn.prog < 0 ? '−' : '+'}${Math.abs(sn.prog)}`}</td>
                  <td className="t">{sn.cancelled || sn.prog == null ? '–' : fmt(selStop.planned + sn.prog)}</td>
                  <td className="t">{sn.plat}</td>
                </tr>
              ))}
            </tbody>
          </table></div>
        </div>
      </div>
    </section>
  )
}
