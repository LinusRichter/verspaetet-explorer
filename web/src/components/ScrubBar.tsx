// Knowledge-time slider. Hand-built instead of <input type=range>: thumb,
// ticks and the end-of-data marker share the same percentage math in the
// same container, so they cannot drift apart. Keyboard: ←/→ = 5 min.
// Dragging updates the display live; fetches (settledScrub) trail 250 ms
// and fire immediately on release.
//
// Day picker: offers exactly meta.days (the dataset's coverage), default =
// latest. There is no "today" — every day is historical; the marker sits
// at the day's end ("Tagesende", all data final).

import { useRef } from 'react'
import { useApp } from '../state'
import { clamp, dayShort, fmt } from '../lib'

export function ScrubBar(): React.ReactNode {
  const { meta, scrub, setScrub, settleScrub, nav, setDay } = useApp()
  const track = useRef<HTMLDivElement>(null)

  if (!meta) return <div className="scrub-row"><div className="scrub-placeholder" /></div>

  const t0 = meta.t0
  const t1 = meta.t1
  const span = t1 - t0
  const pos = (t: number) => ((t - t0) / span) * 100
  const snap = (t: number) => clamp(Math.round(t / 5) * 5, t0, t1)
  const state = scrub < meta.now ? 'historisch' : 'Tagesende'
  const stateCls = scrub < meta.now ? 'hist' : ''

  const val = (clientX: number): number => {
    const r = track.current?.getBoundingClientRect()
    if (!r || r.width === 0) return scrub
    return t0 + clamp((clientX - r.left) / r.width, 0, 1) * span
  }

  const stepMin = span > 900 ? 120 : 60
  const ticks: number[] = []
  for (let m = Math.ceil(t0 / stepMin) * stepMin; m <= t1; m += stepMin) ticks.push(m)

  // Day options from the dataset's coverage; the selected day is always
  // an option (deep links are validated against meta.days by the backend).
  const dayOptions = meta.days.map((iso) => ({ v: iso, label: dayShort(iso) }))
  const selDay = nav.day ?? meta.day

  return (
    <div className="scrub-row">
      <div className="scrub-head">
        <select className="daysel" value={selDay} onChange={(e) => setDay(e.target.value)}
          title="Tag des Wissensstands" aria-label="Tag">
          {dayOptions.some((o) => o.v === selDay)
            ? dayOptions.map((d) => <option key={d.v} value={d.v}>{d.label}</option>)
            : <option value={selDay}>{dayShort(selDay)}</option>}
        </select>
        <span className="scrub-label">Wissensstand</span>
        <span className="scrub-time">{scrub === 1440 ? '24:00' : fmt(scrub)}</span>
        <span className={`scrub-state ${stateCls}`}>{state}</span>
        <button className="mini scrub-now-sm" onClick={() => setScrub(meta.now)} title="Sprung zum Tagesende">Ende</button>
      </div>
      <div className="scrub-wrap">
        <div
          ref={track}
          className="scrubtrack"
          tabIndex={0}
          role="slider"
          aria-label="Wissensstand"
          aria-valuemin={t0}
          aria-valuemax={t1}
          aria-valuenow={scrub}
          aria-valuetext={fmt(scrub)}
          title="Wissensstand ziehen · ←/→ = 5 min"
          onPointerDown={(e) => {
            e.preventDefault()
            track.current?.setPointerCapture(e.pointerId)
            setScrub(snap(val(e.clientX)), true)
          }}
          onPointerMove={(e) => {
            if (e.buttons) {
              const t = snap(val(e.clientX))
              if (t !== scrub) setScrub(t, true)
            }
          }}
          onPointerUp={() => settleScrub()}
          onPointerCancel={() => settleScrub()}
          onKeyDown={(e) => {
            const s = e.key === 'ArrowLeft' ? -5 : e.key === 'ArrowRight' ? 5 : 0
            if (s) { e.preventDefault(); setScrub(snap(scrub + s), true) }
          }}
        >
          <div className="rail" />
          <div className="thumb" style={{ left: `${pos(clamp(scrub, t0, t1))}%` }} />
        </div>
        <div className="nowmark" style={{ left: `${pos(meta.now)}%` }} title={`Tagesende (${fmt(meta.now)})`} />
        <div className="ticks">
          {ticks.map((m) => <span key={m} style={{ left: `${pos(m)}%` }}>{m === 1440 ? '24:00' : fmt(m)}</span>)}
        </div>
      </div>
      <button className="mini scrub-now" onClick={() => setScrub(meta.now)} title="Sprung zum Tagesende">Ende</button>
    </div>
  )
}
