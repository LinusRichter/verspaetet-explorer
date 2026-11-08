// SVG charts, pure props — no chart library needed.

import { useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import type { LineRun, Meta, Run, HourBar } from '../types'
import type { Palette } from '../theme'
import { delayHex } from '../theme'
import { fmt, fmtSigned } from '../lib'

/** Container width in px (ResizeObserver, measured before the first paint →
 * no jump). The SVG draws 1:1 in pixels, text stays legible on mobile. */
function useWidth(fallback: number): [React.RefObject<HTMLDivElement | null>, number] {
  const ref = useRef<HTMLDivElement>(null)
  const [w, setW] = useState(fallback)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const upd = () => { const cw = Math.round(el.clientWidth); if (cw > 0) setW(cw) }
    upd()
    const ro = new ResizeObserver(upd)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return [ref, w]
}

interface Pt { stop: number; snap: number; at: number; prog: number; ist: boolean }

/** Trip view: delay evolution — X = knowledge time, Y = communicated delay,
 * one step line per stop, one dot per snapshot. A value holds until the next
 * snapshot (step-after). Hover shows snapshot time + value; clicking a dot
 * selects the stop and sets the knowledge time. */
export function EvolutionChart({ run, selIdx, meta, scrub, onScrub, onSelectStop, p }: {
  run: Run; selIdx: number; meta: Meta; scrub: number
  onScrub: (t: number) => void; onSelectStop: (stopIdx: number) => void; p: Palette
}): ReactNode {
  const [boxRef, W] = useWidth(1000)
  const [hover, setHover] = useState<Pt | null>(null)
  const tipRef = useRef<HTMLDivElement>(null)
  const [tipW, setTipW] = useState(0)
  useLayoutEffect(() => {
    if (hover && tipRef.current) setTipW(tipRef.current.offsetWidth)
  }, [hover])
  const H = 230, ML = 38, MR = 14, MT = 12, MB = 26
  // X scale from the RUN's data instead of the day window: first snapshot to
  // last actual/forecast + padding, quantized to the 30-min grid.
  const xsAll: number[] = []
  for (const s of run.stops) { xsAll.push(s.planned, s.actual); for (const sn of s.snaps) xsAll.push(sn.at) }
  const hasData = xsAll.length > 0
  const xT0 = hasData ? Math.floor((Math.min(...xsAll) - 20) / 30) * 30 : meta.t0
  const xT1 = hasData ? Math.ceil((Math.max(...xsAll) + 20) / 30) * 30 : meta.t1
  const span = Math.max(1, xT1 - xT0)

  // All snapshot points (only those with a value; cancellation snapshots have none).
  const pts: Pt[][] = run.stops.map((st, si) => st.snaps
    .map((sn, k) => (sn.prog == null ? null : { stop: si, snap: k, at: sn.at, prog: sn.prog, ist: sn.ist }))
    .filter((v): v is Pt => v != null))

  // Y scale from ALL communicated values (forecast spikes are the core of
  // the dataset — not just the final delay). Negative forecasts are real
  // communicated early times and stay visible.
  const vals = [...pts.flat().map((v) => v.prog), ...run.stops.filter((s) => !s.cancelled).map((s) => s.finalD)]
  const maxD = Math.max(15, ...vals) + 3
  const vMin = Math.min(0, ...vals)
  const range = maxD - vMin
  const dStep = range > 120 ? 20 : range > 30 ? 10 : 5
  const minD = Math.floor(vMin / dStep) * dStep
  const plotW = Math.max(10, W - ML - MR)
  const x = (t: number) => ML + ((t - xT0) / span) * plotW
  const y = (d: number) => H - MB - ((d - minD) / (maxD - minD)) * (H - MT - MB)
  // Tick spacing for the real width: labels never denser than ~48 px.
  const stepMin = [30, 60, 120, 180, 240, 360, 720].find((s) => (s / span) * plotW >= 48) ?? 720
  const ticks: number[] = []
  for (let m = Math.ceil(xT0 / stepMin) * stepMin; m <= xT1; m += stepMin) ticks.push(m)

  const stepPath = (ps: Pt[]): string => {
    if (!ps.length) return ''
    let d = `M${x(ps[0].at).toFixed(1)},${y(ps[0].prog).toFixed(1)}`
    for (let k = 1; k < ps.length; k++) d += `H${x(ps[k].at).toFixed(1)}V${y(ps[k].prog).toFixed(1)}`
    return d
  }

  // Nearest point to the pointer (≤ 14 px); on ties the selected stop wins.
  const nearest = (e: React.PointerEvent<SVGSVGElement> | React.MouseEvent<SVGSVGElement>): Pt | null => {
    const r = e.currentTarget.getBoundingClientRect()
    if (!r.width || !r.height) return null
    const px = ((e.clientX - r.left) / r.width) * W
    const py = ((e.clientY - r.top) / r.height) * H
    let best: Pt | null = null, bd = 14 * 14
    for (const ps of pts) for (const q of ps) {
      const dx = x(q.at) - px, dy = y(q.prog) - py
      const dd = dx * dx + dy * dy - (q.stop === selIdx ? 4 : 0)
      if (dd < bd) { bd = dd; best = q }
    }
    return best
  }

  const click = (e: React.MouseEvent<SVGSVGElement>) => {
    const hit = nearest(e)
    if (hit) {
      if (hit.stop !== selIdx) onSelectStop(hit.stop)
      onScrub(hit.at)
      return
    }
    const r = e.currentTarget.getBoundingClientRect()
    const xIn = ((e.clientX - r.left) / r.width) * W
    onScrub(Math.round((xT0 + Math.max(0, Math.min(1, (xIn - ML) / plotW)) * span) / 5) * 5)
  }

  const order = run.stops.map((_, i) => i).filter((i) => i !== selIdx)
  if (selIdx >= 0 && selIdx < run.stops.length) order.push(selIdx) // selected stop on top

  const hs = hover ? run.stops[hover.stop] : null
  const hx = hover ? x(hover.at) : 0
  const hy = hover ? y(hover.prog) : 0

  return (
    <div className="chartbox" ref={boxRef}>
      <svg width={W} height={H} viewBox={`0 0 ${W} ${H}`}
        style={{ width: '100%', height: H, display: 'block', cursor: hover ? 'pointer' : 'crosshair' }}
        onClick={click}
        onPointerMove={(e) => setHover(nearest(e))}
        onPointerLeave={() => setHover(null)}>
        <text x={ML - 6} y={MT + 8} textAnchor="end" fontSize={10} fill={p.label}>min</text>
        {Array.from({ length: Math.floor((maxD - minD) / dStep) + 1 }, (_, i) => minD + i * dStep).map((d) => (
          <g key={d}>
            <line x1={ML} y1={y(d)} x2={W - MR} y2={y(d)} stroke={d === 0 ? p.axis : p.grid} strokeWidth={d === 0 ? 1.5 : 1} />
            {d % (dStep * 2) === 0 && <text x={ML - 6} y={y(d) + 3} textAnchor="end" fontSize={10} fill={p.label}>{d}</text>}
          </g>
        ))}
        {ticks.map((m) => (
          <g key={m}>
            <line x1={x(m)} y1={H - MB} x2={x(m)} y2={H - MB + 4} stroke={p.axis} />
            <text x={x(m)} y={H - MB + 16} textAnchor="middle" fontSize={10} fill={p.label}>{fmt(m)}</text>
          </g>
        ))}
        {meta.now >= xT0 && meta.now <= xT1 && (<>
          <line x1={x(meta.now)} y1={MT} x2={x(meta.now)} y2={H - MB} stroke={p.now} strokeDasharray="4 3" opacity={0.65} />
          <text x={x(meta.now) + 4} y={MT + 10} fontSize={10} fill={p.now}>Ende</text>
        </>)}
        {order.map((i) => {
          const st = run.stops[i]
          const ps = pts[i]
          const sel = i === selIdx
          const col = sel ? p.polySel : p.poly
          const last = st.snaps[st.snaps.length - 1]
          const r = sel ? 3.5 : 2.5
          return (
            <g key={i}>
              {ps.length > 1 && <path d={stepPath(ps)} fill="none" stroke={col} strokeWidth={sel ? 2 : 1} />}
              {ps.map((q) => q.ist
                ? <rect key={q.snap} x={x(q.at) - r} y={y(q.prog) - r} width={2 * r} height={2 * r} fill={delayHex(p, q.prog)} />
                : <circle key={q.snap} cx={x(q.at)} cy={y(q.prog)} r={sel ? 3 : 2} fill={col} />)}
              {st.cancelled && last && <text x={x(last.at) + 3} y={y(0) - 4} fontSize={10} fill={p.bad}>✕</text>}
            </g>
          )
        })}
        {scrub >= xT0 && scrub <= xT1 && (
          <line x1={x(scrub)} y1={MT} x2={x(scrub)} y2={H - MB} stroke={p.polySel} strokeWidth={2} opacity={0.8} pointerEvents="none" />)}
        {hover && <circle cx={hx} cy={hy} r={6} fill="none" stroke={p.polySel} strokeWidth={1.5} pointerEvents="none" />}
      </svg>
      {hover && hs && (
        <div className="charttip" ref={tipRef} style={{
          top: Math.max(0, hy - 64),
          // left/right of the point, but always inside the chart box (mobile!)
          left: Math.max(0, Math.min(hx > W / 2 ? hx - 10 - tipW : hx + 10, W - tipW)),
        }}>
          <b>{hs.name}</b> <span className="m">· Plan {fmt(hs.planned)}</span><br />
          <span className="m">Stand</span> {fmt(hover.at)}
          <span className="m"> · Snapshot {hover.snap + 1}/{hs.snaps.length}</span><br />
          {hover.ist ? 'Ist' : 'kommuniziert'}{' '}
          <b style={{ color: delayHex(p, hover.prog) }}>{fmtSigned(hover.prog)} min</b>
          <span className="m"> → {fmt(hs.planned + hover.prog)}</span>
        </div>
      )}
    </div>
  )
}

/** Line: spaghetti — X = stop position, Y = final delay, one line per run. */
export function SpaghettiChart({ runs, p }: { runs: LineRun[]; p: Palette }): ReactNode {
  const W = 1000, H = 292, ML = 38, MR = 14, MT = 12, MB = 92 // MB: space for the rotated station names (−38°, up to ~50 px below the baseline)
  const live = runs.filter((r) => !r.cancelled)
  const n = runs[0]?.stops.length ?? 1
  const maxD = Math.max(15, ...live.map((r) => r.finalD)) + 3
  const x = (i: number) => ML + (n === 1 ? 0 : (i / (n - 1)) * (W - ML - MR))
  const y = (d: number) => H - MB - (d / maxD) * (H - MT - MB)
  const dStep = maxD > 30 ? 10 : 5
  let worst: LineRun | undefined, best: LineRun | undefined
  for (const r of live) {
    if (!worst || r.finalD > worst.finalD) worst = r
    if (!best || r.finalD < best.finalD) best = r
  }
  const every = Math.max(1, Math.round(n / 14))

  return (
    <svg viewBox={`0 0 ${W} ${H}`} style={{ width: '100%', height: 'auto', display: 'block' }}>
      {Array.from({ length: Math.floor(maxD / dStep) + 1 }, (_, i) => i * dStep).map((d) => (
        <g key={d}>
          <line x1={ML} y1={y(d)} x2={W - MR} y2={y(d)} stroke={p.grid} />
          {d % (dStep * 2) === 0 && <text x={ML - 6} y={y(d) + 3} textAnchor="end" fontSize={10} fill={p.label}>{d}</text>}
        </g>
      ))}
      {runs[0]?.stops.map((st, i) => {
        if (i % every !== 0 && i !== n - 1) return null
        const xx = x(i)
        return (
          <g key={i}>
            <line x1={xx} y1={MT} x2={xx} y2={H - MB} stroke={p.grid} />
            <text x={xx} y={H - MB + 12} textAnchor="end" fontSize={10} fill={p.label}
              transform={`rotate(-38 ${xx} ${H - MB + 12})`}>{st.name.slice(0, 16)}</text>
          </g>
        )
      })}
      {live.map((r) => {
        const col = r === worst ? p.bad : r === best ? p.ok : p.poly
        const w = r === worst || r === best ? 2 : 1
        const pts = r.stops.map((st, i) => `${x(i).toFixed(1)},${y(st.finalD).toFixed(1)}`).join(' ')
        return <polyline key={r.id} points={pts} fill="none" stroke={col} strokeWidth={w} opacity={r === worst || r === best ? 1 : 0.7}>
          <title>{`Fahrt ${fmt(r.dep)} → ${fmtSigned(r.finalD)}`}</title>
        </polyline>
      })}
    </svg>
  )
}

/** Day overview: average final delay by departure hour. */
export function HourProfile({ hours, p }: { hours: HourBar[]; p: Palette }): ReactNode {
  const W = 1000, H = 170, ML = 38, MR = 14, MT = 12, MB = 24
  const mx = Math.max(4, ...hours.map((h) => h.avg))
  const x = (i: number) => ML + (hours.length === 1 ? 0.5 : (i / (hours.length - 1)) * (W - ML - MR))
  const y = (v: number) => H - MB - (v / mx) * (H - MT - MB)
  const gstep = mx > 16 ? 5 : 2
  const bw = Math.max(6, ((W - ML - MR) / hours.length) * 0.55)

  return (
    <svg viewBox={`0 0 ${W} ${H}`} style={{ width: '100%', height: 'auto', display: 'block' }}>
      {Array.from({ length: Math.floor(mx / gstep) + 1 }, (_, i) => i * gstep).map((d) => (
        <g key={d}>
          <line x1={ML} y1={y(d)} x2={W - MR} y2={y(d)} stroke={p.grid} />
          <text x={ML - 6} y={y(d) + 3} textAnchor="end" fontSize={10} fill={p.label}>{d}</text>
        </g>
      ))}
      {hours.map((h, i) => (
        <g key={h.h}>
          <rect x={x(i) - bw / 2} y={y(h.avg)} width={bw} height={H - MB - y(h.avg)}
            fill={h.avg >= 8 ? p.bad : h.avg >= 4 ? p.orange : p.ok} opacity={0.85}>
            <title>{`ab ${fmt(h.h * 60)} · Ø ${fmtSigned(h.avg)} min · ${h.n} Fahrten`}</title>
          </rect>
          {h.h % 2 === 0 && <text x={x(i)} y={H - MB + 14} textAnchor="middle" fontSize={9} fill={p.label}>{h.h}</text>}
        </g>
      ))}
    </svg>
  )
}
