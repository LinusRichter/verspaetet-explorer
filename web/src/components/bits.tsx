// Small UI building blocks.

import type { ReactNode } from 'react'
import { delayCls } from '../lib'
import type { StopState } from '../lib'

export function Spinner(): ReactNode {
  return <div className="spin" aria-label="lädt" />
}

export function Loading({ note }: { note?: string }): ReactNode {
  return (
    <div className="loading">
      <Spinner />
      {note && <div className="loading-note">{note}</div>}
    </div>
  )
}

export function ErrorBox({ msg }: { msg: string }): ReactNode {
  return <div className="errorbox">Fehler: {msg}</div>
}

export function Empty({ hint }: { hint: string }): ReactNode {
  return <div className="empty">{hint}</div>
}

export function Card({ value, label }: { value: ReactNode; label: string }): ReactNode {
  return (
    <div className="card">
      <b>{value}</b>
      <span className="lbl">{label}</span>
    </div>
  )
}

/** Delay chip: color-coded, title explains actual/forecast.
 * compact (stop tiles): cancellation as a narrow "✕" with the same footprint
 * as "+0" — the full "✕ entfällt" text (81 px) would grow the tile by 2 px
 * on appearance and shift everything. */
export function Chip({ st, compact }: { st: StopState; compact?: boolean }): ReactNode {
  if (!st.known) return <span className="chip unknown" title="noch keine Prognose">–</span>
  if (st.cancelled) {
    if (compact) return <span className="chip cx" title="Halt entfällt">✕</span>
    return <span className="chip cx" title="Halt entfällt">✕ entfällt</span>
  }
  if (st.delay == null) return <span className="chip unknown">–</span>
  const lbl = st.delay < 0 ? `\u2212${Math.abs(st.delay)}` : `+${st.delay}`
  const ttl = `${st.ist ? 'Ist' : 'Prognose'}: ${lbl} min`
  return <span className={`chip ${delayCls(st.delay)}`} title={ttl}>{lbl}</span>
}
