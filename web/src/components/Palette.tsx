// Command palette: the big search over trips, lines and stations.
// Open: Ctrl+K (everywhere) or "/" outside input fields (or header button).
// Trips jump straight into the trip view (day + stop selected), lines into
// the line tab, stations into the station board.
// Keyboard: ↑/↓ select (across all groups), Enter opens, Esc closes.
// Request hygiene: 250 ms debounce (effect + cleanup, stable across
// renders), AbortController, 60-s cache in api.ts (key q|day). Enter during
// a running search waits for THAT response — never a hit of the previous one.

import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api } from '../api'
import type { SearchResults } from '../types'
import { useApp } from '../state'
import { dayShort, errMsg, fmt, isAbort, parseHHMM } from '../lib'
import { Spinner } from './bits'

interface Item {
  key: string
  title: string
  sub?: string
  ctx: string
  act: () => void
}
interface Group { id: string; head: string; items: Item[] }

const EXAMPLES = ['ICE 579', 'horrem s12 14:30', 'köln hbf', 'RE 7']

function minLen(q: string): boolean {
  return q.length >= 2 || /^\d$/.test(q)
}

export function Palette({ open, onClose }: { open: boolean; onClose: () => void }): ReactNode {
  const { nav, goStation, goLine, goRun } = useApp()
  const [q, setQ] = useState('')
  const [res, setRes] = useState<{ q: string; r: SearchResults | null } | null>(null)
  const [sel, setSel] = useState(0)
  const [failed, setFailed] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const pendingEnter = useRef(false)
  const query = q.trim()

  // On open: reset and focus.
  useEffect(() => {
    if (open) {
      setQ('')
      setRes(null)
      setSel(0)
      pendingEnter.current = false
      requestAnimationFrame(() => inputRef.current?.focus())
    }
  }, [open])

  // Search: debounced + abortable. The result is stored with its query.
  useEffect(() => {
    if (!open || !minLen(query)) return
    const ac = new AbortController()
    const t = setTimeout(() => {
      api.search(query, nav.day, ac.signal)
        .then((r) => { setRes({ q: query, r }); setFailed(null) })
        .catch((e) => { if (!isAbort(e)) { setRes({ q: query, r: null }); setFailed(errMsg(e)) } })
    }, 250)
    return () => { clearTimeout(t); ac.abort() }
  }, [query, nav.day, open])

  // Index still building (pending): re-ask once after 2.5 s — only if the
  // query is still current then; abortable via the cleanup.
  const retried = useRef<string | null>(null)
  useEffect(() => {
    if (!open || !res || res.q !== query || !res.r?.pending || retried.current === query) return
    const q0 = query
    const ac = new AbortController()
    const t = setTimeout(() => {
      retried.current = q0
      api.search(q0, nav.day, ac.signal)
        .then((r) => setRes({ q: q0, r }))
        .catch(() => { /* erstes Ergebnis bleibt stehen */ })
    }, 2500)
    return () => { clearTimeout(t); ac.abort() }
  }, [res, query, nav.day, open])

  // Only results for the CURRENT query count.
  const cur = res && res.q === query ? res.r : null
  const loading = minLen(query) && (!res || res.q !== query)

  const groups = useMemo<Group[]>(() => {
    if (!cur) return []
    const close = (f: () => void) => () => { onClose(); f() }
    const g: Group[] = []
    const trips = cur.trips ?? []
    if (trips.length) {
      g.push({
        id: 'trip', head: 'Fahrten → Zuglauf',
        items: trips.map((t) => ({
          key: `trip-${t.runId}-${t.stopIdx ?? 0}`,
          title: t.label,
          sub: t.number && !t.label.includes(t.number) ? `Zug ${t.number}` : undefined,
          ctx: `${t.dep}${t.station ? ` ab ${t.station}` : ''} · ${t.from} → ${t.to}${dayHint(t.day)}`,
          act: close(() => goRun(t.runId, t.stopIdx ?? 0, t.station ? undefined : parseHHMM(t.dep) ?? undefined,
            { line: t.key, day: t.day, station: t.station ?? null })),
        })),
      })
    }
    const las = cur.lineAtStation ?? []
    if (las.length) {
      g.push({
        id: 'las', head: 'Linie an Bahnhof → Linien-Ansicht',
        items: las.map((la) => ({
          key: `las-${la.line.key}-${la.station}`,
          title: `${la.line.label} · ${la.station}`,
          ctx: cur.time != null ? `Wissensstand ${fmt(cur.time)}` : 'Fahrten der Linie an diesem Bahnhof',
          act: close(() => goLine(la.line.key, la.station, cur.time ?? null)),
        })),
      })
    }
    if (cur.lines.length) {
      g.push({
        id: 'line', head: 'Linien → Linien-Ansicht',
        items: cur.lines.map((l) => ({
          key: `line-${l.key}`, title: l.label,
          ctx: `${l.ctx}${l.n ? ` · ${l.n} Fahrt${l.n > 1 ? 'en' : ''}` : ''}`,
          act: close(() => goLine(l.key, null)),
        })),
      })
    }
    if (cur.stations.length) {
      g.push({
        id: 'st', head: `Bahnhöfe → Bahnhofstafel${cur.time != null ? ` · Wissensstand ${fmt(cur.time)}` : ''}`,
        items: cur.stations.map((s) => ({
          key: `st-${s.name}`, title: s.name,
          ctx: (s.n > 0 ? `${s.n} Fahrten · ` : '') + (cur.time != null ? `Tafel zum Wissensstand ${fmt(cur.time)}` : 'Bahnhofstafel'),
          act: close(() => goStation(s.name, cur.time ?? null)),
        })),
      })
    }
    const sl = cur.stationLines ?? []
    if (sl.length && cur.stationName) {
      const st = cur.stationName
      g.push({
        id: 'sl', head: `Linien an „${st}“ → Linien-Ansicht`,
        items: sl.map((l) => ({
          key: `sl-${l.key}`, title: l.label, ctx: `hält an ${st}`,
          act: close(() => goLine(l.key, st, cur.time ?? null)),
        })),
      })
    }
    return g
  }, [cur, onClose, goLine, goStation, goRun])

  const items = useMemo(() => groups.flatMap((g) => g.items), [groups])

  useEffect(() => { setSel(0) }, [query])

  // Enter was pressed while loading → open as soon as THAT response arrives.
  useEffect(() => {
    if (!pendingEnter.current || loading) return
    pendingEnter.current = false
    items[0]?.act()
  }, [loading, items])

  if (!open) return null

  const pick = (i: number) => { items[i]?.act() }

  let idx = 0
  return (
    <div className="palbackdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="palette" role="dialog" aria-label="Suche">
        <div className="palin">
          <span className="palico">⌕</span>
          <input
            ref={inputRef}
            type="text"
            placeholder="Zugnummer, Linie, Bahnhof, Uhrzeit …"
            aria-label="Suche"
            value={q}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => { setQ(e.target.value); pendingEnter.current = false }}
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown') { e.preventDefault(); setSel((s) => Math.min(s + 1, Math.max(0, items.length - 1))) }
              else if (e.key === 'ArrowUp') { e.preventDefault(); setSel((s) => Math.max(s - 1, 0)) }
              else if (e.key === 'Enter') {
                e.preventDefault()
                if (loading) pendingEnter.current = true
                else pick(sel)
              } else if (e.key === 'Escape') { e.preventDefault(); onClose() }
            }}
          />
          <span className="palhint">Esc schließt</span>
        </div>
        <div className="palres">
          {!loading && cur?.pending && (
            <div className="palpending">Index wird aufgebaut … Linien und Fahrten können noch fehlen, Suche wird gleich wiederholt.</div>
          )}
          {loading && <Spinner />}
          {!loading && items.length === 0 && minLen(query) && (
            <div className="palempty">
              {failed ? (
                <p>Suche fehlgeschlagen: {failed}</p>
              ) : (
                <>
                  <p>Nichts gefunden für „{query}“{nav.day ? ` am ${dayShort(nav.day)}` : ' (heute)'}.</p>
                  <p>Linien und Fahrten kommen aus den Daten des gewählten Tages — anderen Tag im Wissensstand-Feld wählen,
                    oder z. B. so suchen:</p>
                  <div className="ex">
                    {EXAMPLES.map((ex) => <button key={ex} onClick={() => { setQ(ex); inputRef.current?.focus() }}>{ex}</button>)}
                  </div>
                </>
              )}
            </div>
          )}
          {!loading && groups.map((g) => (
            <div key={g.id}>
              <div className="sgroup">{g.head}</div>
              {g.items.map((it) => {
                const i = idx++
                return <PalRow key={it.key} item={it} idx={i} sel={sel} setSel={setSel} pick={pick} />
              })}
            </div>
          ))}
        </div>
        <div className="palkeys"><span>↑↓ wählen</span><span>Enter öffnen</span><span>Ctrl+K / „/“ öffnet</span></div>
      </div>
    </div>
  )
}

/** Day hint for trips that are not on the current day. */
function dayHint(day: string): string {
  return day ? ` · ${dayShort(day)}` : ''
}

function PalRow({ item, idx, sel, setSel, pick }: {
  item: Item; idx: number; sel: number; setSel: (n: number) => void; pick: (i: number) => void
}): ReactNode {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (idx === sel) ref.current?.scrollIntoView({ block: 'nearest' })
  }, [idx, sel])
  return (
    <div
      ref={ref}
      className={`sitem${idx === sel ? ' hi' : ''}`}
      onMouseEnter={() => setSel(idx)}
      onClick={() => pick(idx)}
    >
      <span><b>{item.title}</b>{item.sub && <span className="num">{item.sub}</span>}</span>
      <span className="ctx">{item.ctx}</span>
    </div>
  )
}
