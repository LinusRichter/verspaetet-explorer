// Day overview: KPIs, hourly profile, line ranking, top delays.

import type { ReactNode } from 'react'
import { useFetch } from '../useFetch'
import { api } from '../api'
import type { DayData } from '../types'
import { useApp } from '../state'
import { clamp, dayLong, fmt, fmtNum, fmtSigned } from '../lib'
import { delayHex, palette } from '../theme'
import { Card, ErrorBox, Loading } from './bits'
import { HourProfile } from './charts'

export function DayView(): ReactNode {
  const { meta, nav, goLine, goRun, theme } = useApp()
  const p = palette(theme)
  // Data belongs to the day: never show the old KPIs under a new date.
  const { data, err } = useFetch<DayData>(`day|${nav.day ?? ''}`, (s) => api.day(nav.day, s))

  if (err) return <ErrorBox msg={err} />
  if (!data) return <Loading note="Übersicht wird geladen — Tagesaggregate werden berechnet (kann dauern), dann 1 min im Browser gecacht …" />

  return (
    <section>
      <div className="vhead">
        <h2>{meta ? dayLong(meta.day) : ''}</h2>
        <span className="muted">
          {fmtNum(data.kpis.runs)} Fahrten · {data.leaderboard.length} Linien · {fmtNum(data.kpis.snaps)} Snapshots
        </span>
      </div>

      <div className="cards">
        <Card value={fmtNum(data.kpis.runs)} label="Fahrten" />
        <Card value={`${data.kpis.punc} %`} label="pünktlich (≤5)" />
        <Card value={`${fmtSigned(data.kpis.avg)} min`} label="Ø Endverspätung" />
        <Card value={`${fmtNum(data.kpis.cx)} Fahrten`} label="komplett ausgefallen" />
        <Card value={fmtNum(data.kpis.snaps)} label="Snapshots gesammelt" />
      </div>

      <div className="panel">
        <h3>Ø Endverspätung nach Abfahrtsstunde</h3>
        <HourProfile hours={data.hours} p={p} />
      </div>

      <div className="grid2">
        <div className="panel">
          <h3>Linien-Rangliste (nach P90)</h3>
          <div className="tblwrap"><table className="tbl">
            <thead>
              <tr><th>Linie</th><th className="t">Fahrten</th><th className="t">P50</th><th className="t">P90</th><th className="t">max</th><th className="t">pünktl.</th><th className="t">✕</th></tr>
            </thead>
            <tbody>
              {data.leaderboard.map((l) => (
                <tr key={l.key} style={{ cursor: 'pointer' }} onClick={() => goLine(l.key, null)}>
                  <td>{l.label}</td>
                  <td className="t">{l.n}</td>
                  <td className="t">{fmtSigned(l.p50)}</td>
                  <td className="t" style={{ color: delayHex(p, l.p90) }}>{fmtSigned(l.p90)}</td>
                  <td className="t">{fmtSigned(l.max)}</td>
                  <td className="t">{l.punc}%</td>
                  <td className="t">{l.cx}</td>
                </tr>
              ))}
            </tbody>
          </table></div>
        </div>

        <div className="panel">
          <h3>Top-Verspätungen (Klick → Zuglauf)</h3>
          {data.top.map((t) => (
            <div key={t.runId} className="topline" onClick={() => goRun(t.runId, 0, t.dep, { station: null, line: null })}>
              <span className="bar" style={{ width: clamp((t.finalD / 45) * 140, 4, 140), background: delayHex(p, t.finalD) }} />
              <span>{t.label} · {fmt(t.dep)} · {t.from} → {t.to}</span>
              <span style={{ marginLeft: 'auto', color: delayHex(p, t.finalD) }}>{fmtSigned(t.finalD)} min</span>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
