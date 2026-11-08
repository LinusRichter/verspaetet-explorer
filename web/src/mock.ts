// Dev mock: serves the same data as the API endpoints, generated entirely
// in the browser. NEVER included in the production build (see the dynamic
// import in api.ts behind import.meta.env.DEV). No UI switches, no fake
// branding — just a data source during development.
//
// Deliberately aligned with the backend semantics (otherwise dev mode
// hides production bugs): stop index 1-based (IRIS), /api/line with a
// station → 404 when the line does not stop there; search returns trips;
// negative communicated early times and forecast spikes occur.

import type { AltLine, BoardData, DayData, JourneyData, LineData, LineInfo, Meta, Run, SearchResults, SearchTrip } from './types'
import { addDays, fmt, pct } from './lib'

function mulberry32(a: number): () => number {
  return function () {
    a |= 0; a = (a + 0x6D2B79F5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}
const rng = mulberry32(20261017)
const ri = (a: number, b: number) => a + Math.floor(rng() * (b - a + 1))
const ch = (p: number) => rng() < p
const clamp = (v: number, a: number, b: number) => Math.max(a, Math.min(b, v))

const SNAP_STEP = 40  // snapshot spacing: 40 min
const PROG_WIN = 240  // forecast visibility window: ~4 h (fchg rows)
const T0 = 300, T1 = 1440, NOW = 14 * 60 + 37

// The mock's dataset is frozen in time: the 7 days ending at the seed day
// (the RNG seed encodes that date). Like the real backend there is no
// "today" — the default day is always the latest one.
const MOCK_DAY = '2026-10-17'
const MOCK_DAYS = Array.from({ length: 7 }, (_, i) => addDays(MOCK_DAY, i - 6))

interface LineDef extends LineInfo {
  evu: string
  first: number
  interval: number
  nruns: number
  num0: number      // train number of the first run
  numStep: number
  route: [string, number][]
}

const LINES: LineDef[] = [
  { key: 'ICE-579', label: 'ICE 579', cat: 'ICE', evu: 'DB Fernverkehr', first: 360, interval: 120, nruns: 9, num0: 571, numStep: 2,
    route: [['Frankfurt(Main)Hbf', 0], ['Ffm Flughafen Fernbf', 14], ['Mannheim Hbf', 54], ['Karlsruhe Hbf', 104], ['Baden-Baden', 124], ['Freiburg(Breisgau) Hbf', 164], ['Basel SBB', 218]] },
  { key: 'RE7-NRW', label: 'RE 7', cat: 'RE', evu: 'DB Regio NRW', first: 324, interval: 120, nruns: 10, num0: 10101, numStep: 1,
    route: [['Münster(Westf)Hbf', 0], ['Hamm(Westf)Hbf', 32], ['Dortmund Hbf', 55], ['Hagen Hbf', 80], ['Wuppertal Hbf', 108], ['Solingen Hbf', 126], ['Köln Hbf', 158]] },
  { key: 'RE7-SH', label: 'RE 7', cat: 'RE', evu: 'DB Regio Nord', first: 336, interval: 120, nruns: 9, num0: 21001, numStep: 1,
    route: [['Hamburg Hbf', 0], ['Neumünster', 66], ['Kiel Hbf', 96], ['Rendsburg', 126], ['Flensburg', 168]] },
  { key: 'RB-34', label: 'RB 34', cat: 'RB', evu: 'DB Regio', first: 342, interval: 60, nruns: 19, num0: 30401, numStep: 1,
    route: [['Minden(Westf)', 0], ['Bückeburg', 12], ['Stadthagen', 24], ['Wunstorf', 44], ['Hannover Hbf', 62]] },
  { key: 'S-6', label: 'S 6', cat: 'S', evu: 'DB Regio', first: 312, interval: 60, nruns: 19, num0: 31601, numStep: 1,
    route: [['Köln Nippes', 0], ['Köln Hbf', 7], ['Köln Messe/Deutz', 12], ['Porz(Rhein)', 21], ['Troisdorf', 29], ['Siegburg/Bonn', 41]] },
  { key: 'S-12', label: 'S 12', cat: 'S', evu: 'DB Regio', first: 318, interval: 60, nruns: 19, num0: 31201, numStep: 1,
    route: [['Horrem', 0], ['Köln-Ehrenfeld', 14], ['Köln Hbf', 20], ['Köln Messe/Deutz', 24], ['Porz(Rhein)', 33], ['Troisdorf', 41], ['Siegburg/Bonn', 50], ['Hennef(Sieg)', 58]] },
]

interface Snap { at: number; prog: number | null; ist: boolean; cancelled: boolean; plat: string }
interface Stop { name: string; eva: string; idx: number; planned: number; finalD: number; actual: number; cancelled: boolean; platFrom: string; platTo: string; platKnown: number; snaps: Snap[] }
interface MockRun extends Run { line: LineDef; number: string }

function mkSnaps(stop: Stop, spike = 0): Snap[] {
  const snaps: Snap[] = []
  const tEnd = stop.cancelled ? stop.planned + 2 : stop.actual + 2
  for (let at = Math.ceil((stop.planned - PROG_WIN) / SNAP_STEP) * SNAP_STEP; at <= tEnd; at += SNAP_STEP) {
    if (at > T1) break
    if (stop.cancelled && at >= stop.planned - 45) {
      snaps.push({ at, prog: null, ist: false, cancelled: true, plat: stop.platFrom })
      continue
    }
    const dt = stop.planned - at
    let prog: number | null
    if (dt > PROG_WIN) prog = null
    else {
      const conv = clamp((PROG_WIN - dt) / 80, 0, 1)
      const wob = ((ri(0, 14) - 7) / 7) * (1 - conv) * 6
      // negative values are real communicated early times (they occur in production)
      prog = clamp(Math.round(stop.finalD + wob + spike * (1 - conv)), -8, 60)
    }
    snaps.push({ at, prog, ist: false, cancelled: false, plat: at >= stop.platKnown ? stop.platTo : stop.platFrom })
  }
  if (tEnd <= T1) {
    snaps.push({ at: tEnd, prog: stop.cancelled ? null : stop.finalD, ist: !stop.cancelled, cancelled: stop.cancelled, plat: stop.cancelled ? stop.platFrom : stop.platTo })
  }
  return snaps
}

function evaOf(name: string): string {
  let h = 0
  for (const c of name) h = (h * 31 + c.charCodeAt(0)) >>> 0
  return String(8000000 + (h % 999999))
}

function buildRuns(): MockRun[] {
  const runs: MockRun[] = []
  for (const L of LINES) {
    const plats = L.route.length > 6 ? 9 : 6
    for (let k = 0; k < L.nruns; k++) {
      const dep = L.first + k * L.interval
      const id = `${L.key}#${k}`
      const runCx = id === 'RB-34#7' || id === 'S-6#12'
      // forecast spike: +25 communicated midway, little at the end
      const spike = id === 'ICE-579#4' || id === 'S-12#8' ? 25 : 0
      let d = ch(0.18) ? ri(5, 14) : ri(0, 3)
      const stops: Stop[] = L.route.map(([name, off], i) => {
        if (i > 0) {
          if (ch(0.45)) d += ri(1, 3)
          if (ch(0.28)) d -= ri(1, 2)
          d = clamp(d, 0, 45)
        }
        let incident = 0
        if (id === 'RE7-NRW#3' && i >= 3) incident = 14 + 2 * i
        if (id === 'ICE-579#2' && i >= 3 && i <= 4) incident = 12 - 2 * (i - 3)
        const finalD = runCx ? 0 : clamp(d + incident, 0, 45)
        const planned = dep + off
        const actual = runCx ? planned + 2 : planned + finalD
        const cancelled = runCx || ch(0.004)
        const platFrom = String(1 + ((k * 5 + i * 3) % plats))
        let platTo = platFrom
        let platKnown = 1e9
        if ((id === 'S-6#5' && i === 2) || ch(0.012)) {
          platTo = String(1 + ((Number(platFrom) + 2) % plats))
          platKnown = planned - ri(35, 70)
        }
        const stop: Stop = { name, eva: evaOf(name), idx: i + 1, planned, finalD, actual, cancelled, platFrom, platTo, platKnown, snaps: [] }
        stop.snaps = mkSnaps(stop, spike)
        return stop
      })
      runs.push({ id, lineKey: L.key, dep, cancelled: runCx, stops, line: L, number: String(L.num0 + k * L.numStep) })
    }
  }
  return runs
}

const RUNS = buildRuns()
const STATIONS = new Map<string, { r: MockRun; i: number }[]>()
for (const r of RUNS) {
  r.stops.forEach((st, i) => {
    if (!STATIONS.has(st.name)) STATIONS.set(st.name, [])
    STATIONS.get(st.name)!.push({ r, i })
  })
}

function delay(ms: number): Promise<void> {
  return new Promise((res) => setTimeout(res, ms))
}

/* ---------- Search normalization (identical to the Go backend) ---------- */
function normName(s: string): string {
  return s.toLowerCase().replace(/ä/g, 'a').replace(/ö/g, 'o').replace(/ü/g, 'u')
    .replace(/ß/g, 'ss').replace(/[^a-z0-9]/g, '')
}
function normInput(q: string): string[] {
  const base = normName(q)
  const alt = normName(q.replace(/oe/g, 'o').replace(/ue/g, 'u').replace(/ae/g, 'a'))
  return alt === base ? [base] : [base, alt]
}
function matchScore(norm: string, variants: string[]): number {
  let best = -1
  for (const v of variants) {
    if (!v) continue
    if (norm === v) return 0
    if (norm.startsWith(v)) best = best < 0 || best > 1 ? 1 : best
    else if (norm.includes(v)) best = best < 0 ? 2 : Math.min(best, 2)
  }
  return best
}
function parseTimeHint(q: string): { min: number; rest: string } | null {
  const pats = [
    /(^|\s)([01]?\d|2[0-3])\s*(:|\.|h|uhr)\s*([0-5]\d)(?=$|\s|:|\.)/,
    /(^|\s)((?:[01]\d|2[0-3])[0-5]\d)(?=$|\s)/,
    /(^|\s)([01]?\d|2[0-3])\s*uhr(?=$|\s)/,
  ]
  for (const p of pats) {
    const m = p.exec(q)
    if (!m) continue
    const digits = m[0].replace(/[^0-9]/g, '')
    const min = digits.length <= 2 ? Number(digits) * 60 : Math.floor(Number(digits) / 100) * 60 + (Number(digits) % 100)
    return { min, rest: (q.slice(0, m.index) + ' ' + q.slice(m.index + m[0].length)).trim() }
  }
  return null
}

function linesAtStation(name: string): LineDef[] {
  const seen = new Set<string>()
  const out: LineDef[] = []
  for (const { r } of STATIONS.get(name) ?? []) {
    if (!seen.has(r.line.key)) { seen.add(r.line.key); out.push(r.line) }
  }
  return out
}

/** Most relevant run of a line for direct jumps (near "now − 100 min"). */
function bestRun(lineKey: string): { runId: string; dep: number } {
  const rs = RUNS.filter((r) => r.lineKey === lineKey)
  let best = rs[0]
  for (const r of rs) {
    if (Math.abs(r.dep - (NOW - 100)) < Math.abs(best.dep - (NOW - 100))) best = r
  }
  return { runId: best.id, dep: best.dep }
}

/** Quick switch: the station's lines, if the current line stops there. */
function altLines(lineKey: string, station: string | null): { list: AltLine[]; scoped: boolean } {
  const scoped = !!station && linesAtStation(station).some((l) => l.key === lineKey)
  const base = scoped ? linesAtStation(station!) : LINES
  return {
    list: base.map((L) => ({ key: L.key, label: L.label, cat: L.cat, ...bestRun(L.key) })),
    scoped,
  }
}

function searchTokens(q: string, day: string): SearchResults {
  let time: number | null = null
  const th = parseTimeHint(q)
  if (th) { time = th.min; q = th.rest }
  const tokens = q.trim().split(/\s+/).filter(Boolean)
  const out: SearchResults = { stations: [], lines: [] }
  if (tokens.length === 0) { if (time != null) out.time = time; return out }

  let firstStation: string | null = null
  const seenSt = new Set<string>(), seenLi = new Set<string>()
  for (const tok of tokens) {
    const variants = normInput(tok)
    // stations per token
    const sts = [...STATIONS.keys()].filter((n) => matchScore(normName(n), variants) >= 0).slice(0, 3)
    for (const st of sts) {
      if (!seenSt.has(st) && out.stations.length < 8) { seenSt.add(st); out.stations.push({ name: st, n: STATIONS.get(st)?.length ?? 0 }) }
    }
    if (sts.length > 0 && !firstStation) firstStation = sts[0]
    // lines per token
    for (const L of LINES) {
      const numOnly = normName(L.label.replace(/^[A-Z]+\s/, ''))
      const normCat = normName(L.cat) + numOnly
      let score = matchScore(normCat, variants)
      if (numOnly) { const s2 = matchScore(numOnly, variants); if (s2 >= 0 && (score < 0 || s2 + 1 < score)) score = s2 + 1 }
      if (score < 0) continue
      const key = L.key
      if (seenLi.has(key)) continue
      const runs = RUNS.filter((r) => r.lineKey === key)
      const mid = runs[0].stops[Math.floor(runs[0].stops.length / 2)].name
      const line = { key, label: L.label, cat: L.cat, ctx: mid + ' ⇢ ' + runs[0].stops[runs[0].stops.length - 1].name, n: runs.length }
      if (sts.length > 0) {
        // token matched stations: list as a plain line result only while there is room
        if (out.lines.length < 6) { seenLi.add(key); out.lines.push(line) }
      } else if (firstStation && out.stations.length > 0) {
        // token matched ONLY lines + another token a station → combine
        if ((out.lineAtStation?.length ?? 0) < 3) {
          out.lineAtStation = out.lineAtStation ?? []
          out.lineAtStation.push({ line: { key, label: L.label, cat: L.cat }, station: firstStation })
        }
        if (out.lines.length < 6 && !seenLi.has(key)) { seenLi.add(key); out.lines.push(line) }
      } else if (out.lines.length < 6) { seenLi.add(key); out.lines.push(line) }
    }
  }
  if (time != null) out.time = time
  if (out.stations.length > 0) {
    out.stationName = out.stations[0].name
    out.stationLines = linesAtStation(out.stations[0].name).map((L) => ({ key: L.key, label: L.label, cat: L.cat, n: 0, ctx: 'hält hier' }))
  }
  const trips = searchTrips(tokens, out, time, day)
  if (trips.length) out.trips = trips
  return out
}

function tripOf(r: MockRun, day: string, at?: { station: string; i: number }): SearchTrip {
  const L = r.line
  return {
    runId: r.id, day, key: L.key,
    label: L.cat === 'ICE' || L.cat === 'IC' ? `${L.cat} ${r.number}` : L.label,
    cat: L.cat, number: r.number,
    from: r.stops[0].name, to: r.stops[r.stops.length - 1].name,
    dep: fmt(at ? r.stops[at.i].planned : r.dep),
    ...(at ? { station: at.station, stopIdx: r.stops[at.i].idx } : {}),
  }
}

/** Trips like the backend contract: train number ("ICE 579", "579", "10123")
 * or line + station + time ("horrem s12 14:30" → next run from that time). */
function searchTrips(tokens: string[], out: SearchResults, time: number | null, day: string): SearchTrip[] {
  const res: SearchTrip[] = []
  const digits = tokens.map(normName).find((t) => /^[a-z]*\d{3,}$/.test(t))
  if (digits) {
    const num = digits.replace(/^[a-z]+/, '')
    const cat = digits.replace(/\d+$/, '') || tokens.map(normName).find((t) => /^[a-z]+$/.test(t)) || ''
    for (const r of RUNS) {
      if (res.length >= 8) break
      if (r.number !== num) continue
      if (cat && normName(r.line.cat) !== cat) continue
      res.push(tripOf(r, day))
    }
  }
  if (time != null) {
    for (const la of out.lineAtStation ?? []) {
      const cand = RUNS.filter((r) => r.lineKey === la.line.key)
        .map((r) => ({ r, i: r.stops.findIndex((s) => s.name === la.station) }))
        .filter(({ r, i }) => i >= 0 && i < r.stops.length - 1 && r.stops[i].planned >= time)
        .sort((a, b) => a.r.stops[a.i].planned - b.r.stops[b.i].planned)
        .slice(0, 3)
      for (const c of cand) if (res.length < 8) res.push(tripOf(c.r, day, { station: la.station, i: c.i }))
    }
  }
  return res
}

export default {
  async meta(day: string | null): Promise<Meta> {
    await delay(60)
    // Historical dataset like the real backend: the default day is the
    // LATEST available one and "now" is the day's end — no live "today".
    const d = day && MOCK_DAYS.includes(day) ? day : MOCK_DAYS[MOCK_DAYS.length - 1]
    return { day: d, days: [...MOCK_DAYS], t0: T0, t1: T1, now: T1 }
  },

  async search(q: string, day: string, kind?: 'stations'): Promise<SearchResults> {
    await delay(120 + rng() * 120)
    // Multi-token like the backend: "horrem s12" → line at station.
    const r = searchTokens(q, day)
    // kind=stations like the backend: stations only (max. 10), nothing else
    return kind === 'stations' ? { stations: r.stations.slice(0, 10), lines: [] } : r
  },

  async board(station: string, at: number): Promise<BoardData> {
    await delay(200 + rng() * 150)
    const entries = STATIONS.get(station) ?? []
    const lo = at - 150, hi = at + 240
    const lineDefs = linesAtStation(station)
    const rows = entries
      .map(({ r, i }) => ({ r, i, st: r.stops[i] }))
      .filter(({ st }) => st.planned >= lo && st.planned <= hi)
      .sort((a, b) => a.st.planned - b.st.planned)
      .map(({ r, i, st }) => {
        let best: Snap | null = null
        for (const sn of st.snaps) { if (sn.at <= at) best = sn; else break }
        const known = best != null
        return {
          runId: r.id, stopIdx: st.idx, lineKey: r.line.key, lineLabel: r.line.label, planned: st.planned,
          known, delay: best?.prog ?? null, ist: best?.ist ?? false,
          cancelled: best?.cancelled ?? false,
          plat: best?.plat ?? st.platFrom, platFrom: st.platFrom,
          platChanged: known && st.platTo !== st.platFrom && best?.plat === st.platTo,
          dest: r.stops[r.stops.length - 1].name,
          via: r.stops[i + 1]?.name ?? '—',
        }
      })
    return { rows, lines: lineDefs.map(({ key, label, cat }) => ({ key, label, cat })) }
  },

  async journey(runId: string, station: string | null): Promise<JourneyData | null> {
    await delay(200 + rng() * 150)
    const r = RUNS.find((x) => x.id === runId)
    if (!r) return null
    const { line, number: _n, ...run } = r
    void _n
    const alt = altLines(line.key, station)
    return {
      run, line: { key: line.key, label: line.label, cat: line.cat, evu: line.evu },
      altLines: alt.list, altScoped: alt.scoped,
    }
  },

  async line(lineKey: string, station: string | null): Promise<LineData> {
    await delay(250 + rng() * 200)
    const L = LINES.find((l) => l.key === lineKey)
    if (!L) throw new Error('line not found (HTTP 404)')
    // like the backend (filterByStation): if the line does not stop there → 404
    if (station && !linesAtStation(station).some((l) => l.key === lineKey)) {
      throw new Error('line not found on this day (HTTP 404)')
    }
    const rs = RUNS.filter((r) => r.lineKey === lineKey)
    const runs = rs.map((r) => ({
      id: r.id, dep: r.dep, cancelled: r.cancelled,
      finalD: r.stops[r.stops.length - 1].finalD,
      stops: r.stops.map((s) => ({ name: s.name, finalD: s.finalD, cancelled: s.cancelled })),
    }))
    const fin = rs.filter((r) => !r.cancelled).map((r) => r.stops[r.stops.length - 1].finalD)
    const alt = altLines(lineKey, station)
    return {
      line: { key: L.key, label: L.label, cat: L.cat },
      ctx: runs.length ? `${runs[0].stops[Math.floor(runs[0].stops.length / 2)].name} ⇢ ${runs[0].stops[runs[0].stops.length - 1].name}` : '',
      runs,
      altLines: alt.list, altScoped: alt.scoped,
      stats: {
        n: rs.length,
        cx: rs.filter((r) => r.cancelled).length,
        p50: Math.round(pct(fin, 50)),
        p90: Math.round(pct(fin, 90)),
        max: fin.length ? Math.max(...fin) : 0,
        punc: Math.round((fin.filter((d) => d <= 5).length / Math.max(1, fin.length)) * 100),
        gc: rs.reduce((a, r) => a + r.stops.filter((s) => s.platTo !== s.platFrom).length, 0),
      },
    }
  },

  async day(): Promise<DayData> {
    await delay(250 + rng() * 200)
    const finAll = RUNS.filter((r) => !r.cancelled).map((r) => r.stops[r.stops.length - 1].finalD)
    const byHour = new Map<number, number[]>()
    for (const r of RUNS) {
      if (r.cancelled) continue
      const h = Math.floor(r.dep / 60)
      byHour.set(h, [...(byHour.get(h) ?? []), r.stops[r.stops.length - 1].finalD])
    }
    const hours = [...byHour.entries()]
      .sort((a, b) => a[0] - b[0])
      .map(([h, v]) => ({ h, avg: Math.round((v.reduce((a, b) => a + b, 0) / v.length) * 10) / 10, n: v.length }))
    const leaderboard = LINES.map((L) => {
      const rs = RUNS.filter((r) => r.lineKey === L.key)
      const fin = rs.filter((r) => !r.cancelled).map((r) => r.stops[r.stops.length - 1].finalD)
      return {
        key: L.key, label: L.label, n: rs.length,
        p50: Math.round(pct(fin, 50)), p90: Math.round(pct(fin, 90)),
        max: fin.length ? Math.max(...fin) : 0,
        punc: Math.round((fin.filter((d) => d <= 5).length / Math.max(1, fin.length)) * 100),
        cx: rs.filter((r) => r.cancelled).length,
      }
    }).sort((a, b) => b.p90 - a.p90)
    const top = RUNS.filter((r) => !r.cancelled)
      .sort((a, b) => b.stops[b.stops.length - 1].finalD - a.stops[a.stops.length - 1].finalD)
      .slice(0, 8)
      .map((r) => ({
        runId: r.id, label: r.line.label, dep: r.dep,
        from: r.stops[0].name, to: r.stops[r.stops.length - 1].name,
        finalD: r.stops[r.stops.length - 1].finalD,
      }))
    return {
      kpis: {
        runs: RUNS.length,
        punc: Math.round((finAll.filter((d) => d <= 5).length / Math.max(1, finAll.length)) * 100),
        avg: Math.round((finAll.reduce((a, b) => a + b, 0) / Math.max(1, finAll.length)) * 10) / 10,
        cx: RUNS.filter((r) => r.cancelled).length,
        gc: RUNS.reduce((a, r) => a + r.stops.filter((s) => s.platTo !== s.platFrom).length, 0),
        snaps: RUNS.reduce((a, r) => a + r.stops.reduce((b, s) => b + s.snaps.length, 0), 0),
      },
      hours, leaderboard, top,
    }
  },
}
