// Station board: searchable station field, lines of the station as chips,
// departures at the knowledge time. Loads exclusively on demand.
// Fetch hygiene: reacts to settledScrub (250 ms trailing while dragging,
// immediate on release), never before the knowledge time is known; stale
// requests are aborted; responses live 60 s in the api.ts cache.

import { useEffect, useRef, useState, type ReactNode } from 'react'
import { api } from '../api'
import type { BoardData, SearchStation } from '../types'
import { useApp } from '../state'
import { useFetch } from '../useFetch'
import { dayLong, fmt, isAbort } from '../lib'
import { catColor, palette } from '../theme'
import { Chip, Empty, ErrorBox, Loading } from './bits'

export function Board(): ReactNode {
  const { nav, scrub, settledScrub, meta, goStation, goLine, goRun, theme } = useApp()
  const p = palette(theme)
  const [q, setQ] = useState('')
  // Suggestions carry their query: Enter must never open a hit of a stale input.
  const [sugRes, setSugRes] = useState<{ q: string; list: SearchStation[] }>({ q: '', list: [] })
  const [sugOpen, setSugOpen] = useState(false)
  const [sugSel, setSugSel] = useState(0)
  const box = useRef<HTMLDivElement>(null)

  // Station suggestions: debounced (200 ms, stable across renders), abortable.
  useEffect(() => {
    const query = q.trim()
    if (query.length < 2) { setSugRes({ q: query, list: [] }); return }
    const ac = new AbortController()
    const t = setTimeout(() => {
      api.search(query, nav.day, ac.signal, 'stations')
        .then((r) => { setSugRes({ q: query, list: r.stations.slice(0, 10) }); setSugSel(0) })
        .catch((e) => { if (!isAbort(e)) setSugRes({ q: query, list: [] }) })
    }, 200)
    return () => { clearTimeout(t); ac.abort() }
  }, [q, nav.day])

  useEffect(() => {
    const h = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setSugOpen(false) }
    document.addEventListener('mousedown', h)
    return () => document.removeEventListener('mousedown', h)
  }, [])

  const ctx = nav.station ? `${nav.station}|${nav.day ?? ''}` : null
  const key = ctx && settledScrub != null ? `${ctx}|${settledScrub}` : null
  const { data, err, loading } = useFetch<BoardData>(
    key, (s) => api.board(nav.station!, settledScrub!, nav.day, s), ctx)

  const sugFresh = sugRes.q === q.trim()
  const sug = sugFresh ? sugRes.list : []
  const pickSug = (name: string) => { setQ(name); setSugOpen(false); goStation(name) }

  return (
    <section className="view-active">
      <div className="vhead">
        <div className="stcombo" ref={box}>
          <input
            placeholder="Bahnhof suchen…"
            aria-label="Bahnhof suchen"
            value={q}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => { setQ(e.target.value); setSugOpen(true) }}
            onFocus={() => setSugOpen(true)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !sugFresh) { e.preventDefault(); return } // no answer for the current input yet
              if (!sugOpen || sug.length === 0) return
              if (e.key === 'ArrowDown') { e.preventDefault(); setSugSel((s) => Math.min(s + 1, sug.length - 1)) }
              else if (e.key === 'ArrowUp') { e.preventDefault(); setSugSel((s) => Math.max(s - 1, 0)) }
              else if (e.key === 'Enter') { e.preventDefault(); if (sug[sugSel]) pickSug(sug[sugSel].name) }
              else if (e.key === 'Escape') setSugOpen(false)
            }}
          />
          {sugOpen && sug.length > 0 && (
            <div className="stlist">
              {sug.map((s, i) => (
                <div key={s.name} className={`stitem${i === sugSel ? ' hi' : ''}`}
                  onMouseEnter={() => setSugSel(i)}
                  onClick={() => pickSug(s.name)}>
                  <span>{s.name}</span>{s.n > 0 && <span className="n" title="Fahrten mit Halt hier am Tag">{s.n} Fahrten</span>}
                </div>
              ))}
            </div>
          )}
        </div>
        {meta && <span className="muted">{dayLong(meta.day)}</span>}
        <span className="hint">Zeile klicken → Zuglauf · Linie klicken → Linien-Ansicht</span>
        {/* always in the layout (visibility instead of conditional render): no jump */}
        <span className="hint busy" style={{ visibility: data && loading ? 'visible' : 'hidden' }}>aktualisiert …</span>
      </div>

      {!nav.station && <Empty hint="Bahnhof suchen — dann erscheinen die Abfahrten zum Wissensstand." />}

      {nav.station && (
        <>
          {data && (
            <div className="chipsline">
              <span className="muted chipslabel">Linien hier:</span>
              {data.lines.map((l) => (
                <button key={l.key} className="lchip" onClick={() => goLine(l.key, nav.station)}>
                  <span className="dot" style={{ background: catColor(l.cat, p) }} />{l.label}
                </button>
              ))}
            </div>
          )}
          {err && <ErrorBox msg={err} />}
          {!data && !err && <Loading note={`Abfahrten ${nav.station} …`} />}
          {data && (
            <div className="tblwrap">
              <table className="tbl">
                <thead>
                  <tr>
                    <th className="t">Plan</th><th className="t">Ist/Prog</th><th className="t">±</th>
                    <th>Linie</th><th>Richtung</th><th className="hide-sm">über</th><th className="t">Gl.</th>
                  </tr>
                </thead>
                <tbody>
                  {data.rows.map((r) => {
                    const st = { known: r.known, ist: r.ist, delay: r.delay, cancelled: r.cancelled, plat: r.plat }
                    return (
                      <tr key={`${r.runId}-${r.stopIdx}`} className={`brow${r.planned < scrub - 2 ? ' dim' : ''}`}
                        onClick={() => goRun(r.runId, r.stopIdx)}>
                        <td className="t">{fmt(r.planned)}</td>
                        <td className="t">{r.known && r.delay != null ? fmt(r.planned + r.delay) : '–'}</td>
                        <td className="t"><Chip st={st} /></td>
                        <td>
                          <span className="linelink" onClick={(e) => { e.stopPropagation(); goLine(r.lineKey, nav.station) }}>
                            <span className="dot" style={{ background: catColor(data.lines.find((l) => l.key === r.lineKey)?.cat ?? '', p) }} />{r.lineLabel}
                          </span>
                        </td>
                        <td>{r.dest || '–'}</td>
                        <td className="muted hide-sm">{r.via || '–'}</td>
                        <td className="t">
                          {r.plat || '–'}
                          {r.platChanged && <span className="pchg" title={`Gleiswechsel (war Gl. ${r.platFrom})`}>⇄</span>}
                        </td>
                      </tr>
                    )
                  })}
                  {data.rows.length === 0 && (
                    <tr><td colSpan={7} className="muted" style={{ textAlign: 'center', padding: 20 }}>
                      keine Abfahrten im Fenster (−2,5 h … +4 h um Wissensstand)
                    </td></tr>
                  )}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </section>
  )
}
