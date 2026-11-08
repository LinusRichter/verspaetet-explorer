// API client of the data UI. In the production build it calls only the real
// endpoints of the Go server. In dev mode (vite dev without VITE_API=1) the
// mock from mock.ts serves the same contracts — dynamically imported, so
// Vite cuts it from the production build.
//
// Request hygiene:
// - All responses (except meta) live 60 s in a frontend cache — plain TTL,
//   no invalidation. The PROMISE is cached: two concurrent callers (e.g.
//   StrictMode, back-and-forth navigation) share one request.
// - Every caller may pass an AbortSignal. The underlying fetch is only
//   aborted when NO caller is left waiting.

import type { BoardData, DayData, JourneyData, LineData, Meta, SearchResults } from './types'
import { berlinToday } from './lib'

type Mock = {
  meta(day: string | null): Promise<Meta>
  search(q: string, day: string, kind?: 'stations'): Promise<SearchResults>
  board(station: string, at: number): Promise<BoardData>
  journey(runId: string, station: string | null): Promise<JourneyData | null>
  line(lineKey: string, station: string | null): Promise<LineData>
  day(): Promise<DayData>
}

const USE_REAL = !import.meta.env.DEV || !!import.meta.env.VITE_API

let mockPromise: Promise<Mock> | null = null
function getMock(): Promise<Mock> {
  mockPromise ??= import('./mock').then((m) => m.default as Mock)
  return mockPromise
}

/** HTTP error with readable text. 503 carries retryAfter (s) from the header. */
export class ApiError extends Error {
  status: number
  retryAfter: number | null
  constructor(status: number, msg: string, retryAfter: number | null) {
    super(msg)
    this.status = status
    this.retryAfter = retryAfter
  }
}

function readable(status: number, serverMsg: string, retryAfter: number | null): string {
  const detail = serverMsg ? ` (${serverMsg})` : ''
  if (status === 400) return `Ungültige Eingabe${detail}`
  if (status === 404) return `Nicht gefunden${detail}`
  if (status === 503) return `Server ausgelastet (schwere Abfrage läuft) — neuer Versuch in ${retryAfter ?? 10} s`
  return `Serverfehler HTTP ${status}${detail}`
}

async function get<T>(path: string, signal: AbortSignal): Promise<T> {
  const r = await fetch(path, { signal })
  if (!r.ok) {
    // The backend sends {"error": "…"} — show readable text, not just a status code.
    let msg = ''
    try { msg = ((await r.json()) as { error?: string }).error ?? '' } catch { /* not JSON */ }
    const ra = Number(r.headers.get('Retry-After'))
    const retryAfter = r.status === 503 ? (Number.isFinite(ra) && ra > 0 ? ra : 10) : null
    throw new ApiError(r.status, readable(r.status, msg, retryAfter), retryAfter)
  }
  return r.json() as Promise<T>
}

async function call<T>(path: string, viaMock: (m: Mock) => Promise<T>, signal: AbortSignal): Promise<T> {
  if (USE_REAL) return get<T>(path, signal)
  const m = await getMock()
  signal.throwIfAborted()
  return viaMock(m)
}

// ── 60s promise cache with in-flight dedup and shared abort ──────────────
const CACHE_TTL = 60_000

interface Entry {
  ts: number
  p: Promise<unknown>
  ac: AbortController
  waiting: number   // callers with a signal still waiting
  pinned: boolean  // a caller without a signal waits → never abort
  done: boolean
}
const cache = new Map<string, Entry>()

function abortErr(): DOMException {
  return new DOMException('aborted', 'AbortError')
}

function shared<T>(key: string, fn: (s: AbortSignal) => Promise<T>, signal?: AbortSignal): Promise<T> {
  const now = Date.now()
  for (const [k, e] of cache) if (e.done && now - e.ts > CACHE_TTL) cache.delete(k)
  let ent = cache.get(key)
  if (!ent) {
    const e: Entry = { ts: now, ac: new AbortController(), waiting: 0, pinned: false, done: false, p: Promise.resolve() }
    e.p = fn(e.ac.signal).then(
      (v) => { e.done = true; e.ts = Date.now(); return v },
      (err) => { if (cache.get(key) === e) cache.delete(key); throw err }, // never cache errors
    )
    e.p.catch(() => { /* rejection is handled by the callers */ })
    cache.set(key, e)
    ent = e
  }
  const e = ent
  if (e.done) return e.p as Promise<T>
  if (!signal) { e.pinned = true; return e.p as Promise<T> }
  if (signal.aborted) return Promise.reject(abortErr())
  e.waiting++
  return new Promise<T>((resolve, reject) => {
    let settled = false
    const leave = () => { if (!settled) { settled = true; e.waiting-- } }
    const onAbort = () => {
      leave()
      reject(abortErr())
      if (e.waiting <= 0 && !e.pinned && !e.done) {
        e.ac.abort()
        if (cache.get(key) === e) cache.delete(key)
      }
    }
    signal.addEventListener('abort', onAbort, { once: true })
    e.p.then(
      (v) => { signal.removeEventListener('abort', onAbort); leave(); resolve(v as T) },
      (err) => { signal.removeEventListener('abort', onAbort); leave(); reject(err) },
    )
  })
}

// The latest available day, learned from /api/meta responses — resolves a
// null nav.day (fresh page load) before meta has arrived. berlinToday() is
// only a dev-mock fallback.
let latestDay: string | null = null

/** Day for cache keys/requests; null = latest available day. */
function dayOf(day: string | null): string {
  return day ?? latestDay ?? berlinToday()
}

function dayParam(day: string | null): string {
  return day ? `&day=${day}` : ''
}

function stationParam(station: string | null): string {
  return station ? `&station=${encodeURIComponent(station)}` : ''
}

export const api = {
  meta: (day: string | null = null, signal?: AbortSignal) => {
    const ac = new AbortController()
    signal?.addEventListener('abort', () => ac.abort(), { once: true })
    const p = call<Meta>(`/api/meta?${dayParam(day).slice(1)}`, (m) => m.meta(day), ac.signal)
    p.then((m) => { latestDay = m.days[m.days.length - 1] ?? latestDay }, () => { /* caller handles */ })
    return p
  },
  /** Search; always sends the selected day. kind='stations' (board
   * combobox): stations only, never triggers a day index build. Responses
   * with pending (index building) are NOT cached. */
  search: (q: string, day: string | null, signal?: AbortSignal, kind?: 'stations') => {
    const d = dayOf(day)
    const key = `search|${kind ?? ''}|${q}|${d}`
    const p = shared(key,
      (s) => call<SearchResults>(
        `/api/search?q=${encodeURIComponent(q)}&day=${d}${kind ? `&kind=${kind}` : ''}`,
        (m) => m.search(q, d, kind), s),
      signal)
    p.then((r) => { if (r.pending) cache.delete(key) }, () => { /* caller handles */ })
    return p
  },
  board: (station: string, at: number, day: string | null, signal?: AbortSignal) =>
    shared(`board|${station}|${at}|${dayOf(day)}`,
      (s) => call<BoardData>(`/api/board?station=${encodeURIComponent(station)}&at=${at}${dayParam(day)}`,
        (m) => m.board(station, at), s),
      signal),
  journey: (runId: string, station: string | null, day: string | null, signal?: AbortSignal) =>
    shared(`journey|${runId}|${station ?? ''}|${dayOf(day)}`,
      (s) => call<JourneyData | null>(
        `/api/journey?run=${encodeURIComponent(runId)}${stationParam(station)}`,
        (m) => m.journey(runId, station), s),
      signal),
  line: (lineKey: string, station: string | null, day: string | null, signal?: AbortSignal) =>
    shared(`line|${lineKey}|${station ?? ''}|${dayOf(day)}`,
      (s) => call<LineData>(
        `/api/line?line=${encodeURIComponent(lineKey)}${stationParam(station)}${dayParam(day)}`,
        (m) => m.line(lineKey, station), s),
      signal),
  day: (day: string | null, signal?: AbortSignal) =>
    shared(`day|${dayOf(day)}`,
      (s) => call<DayData>(`/api/day${day ? `?day=${day}` : ''}`, (m) => m.day(), s),
      signal),
}
