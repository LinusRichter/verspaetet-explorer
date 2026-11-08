// Small pure helpers, used everywhere.

export function pad(n: number): string {
  return String(n).padStart(2, '0')
}

/** Minutes of the day → "HH:MM". Safe for negative/overflow (midnight trips). */
export function fmt(m: number): string {
  const v = ((m % 1440) + 1440) % 1440
  return pad(Math.floor(v / 60)) + ':' + pad(v % 60)
}

export function clamp(v: number, a: number, b: number): number {
  return Math.max(a, Math.min(b, v))
}

export function pct(arr: number[], p: number): number {
  if (!arr.length) return 0
  const s = [...arr].sort((a, b) => a - b)
  return s[Math.min(s.length - 1, Math.floor((p / 100) * s.length))]
}

/** Knowledge about one stop at time t (last snapshot row ≤ t). */
export interface StopState {
  known: boolean
  ist: boolean
  delay: number | null
  cancelled: boolean
  plat: string
}

export interface SnapLike {
  at: number
  prog: number | null
  ist: boolean
  cancelled: boolean
  plat: string
}

export function stateAt(stop: { snaps: SnapLike[]; platFrom: string }, t: number): StopState {
  let best: SnapLike | null = null
  for (const sn of stop.snaps) {
    if (sn.at <= t) best = sn
    else break
  }
  if (!best) return { known: false, ist: false, delay: null, cancelled: false, plat: stop.platFrom }
  return { known: true, ist: best.ist, delay: best.prog, cancelled: best.cancelled, plat: best.plat }
}

export function delayCls(d: number | null): string {
  if (d == null) return 'unknown'
  if (d >= 10) return 'bad'
  if (d >= 6) return 'orange'
  if (d >= 3) return 'warn'
  return 'ok'
}

export function runStateAt(run: { cancelled: boolean; stops: { name: string; planned: number; actual: number }[] }, t: number): string {
  const st = run.stops
  if (run.cancelled) return 'Fahrt entfallen'
  if (t < st[0].planned - 4) return 'noch nicht abgefahren'
  if (t >= st[st.length - 1].actual) return 'Fahrt beendet'
  for (let i = 0; i < st.length - 1; i++) {
    if (t >= st[i].planned && t < st[i + 1].planned) return `unterwegs ${st[i].name} → ${st[i + 1].name}`
  }
  return 'im Ziel'
}

/** Thousands separators, German style: 42587 → "42.587". */
export function fmtNum(n: number): string {
  return n.toLocaleString('de-DE')
}

/** Signed number: +3 / −2 / ±0 (real minus sign, de-DE decimal comma). */
export function fmtSigned(n: number): string {
  if (n === 0) return '±0'
  const abs = Math.abs(n).toLocaleString('de-DE', { maximumFractionDigits: 1 })
  return (n < 0 ? '−' : '+') + abs
}

// ── Calendar days in Europe/Berlin (not UTC: between 00:00 and 01:00/02:00
// Berlin the UTC date is still "yesterday"). ──────────────────────────────
const berlinFmt = new Intl.DateTimeFormat('en-CA', {
  timeZone: 'Europe/Berlin', year: 'numeric', month: '2-digit', day: '2-digit',
})

/** Today's date in Berlin as YYYY-MM-DD (dev-mock fallback). */
export function berlinToday(now: number = Date.now()): string {
  return berlinFmt.format(new Date(now))
}

/** Plain calendar arithmetic on YYYY-MM-DD (timezone-free via UTC noon). */
export function addDays(iso: string, n: number): string {
  const [y, m, d] = iso.split('-').map(Number)
  return new Date(Date.UTC(y, m - 1, d, 12) + n * 86400000).toISOString().slice(0, 10)
}

const WD = ['So', 'Mo', 'Di', 'Mi', 'Do', 'Fr', 'Sa']

/** "Mo 26.10." */
export function dayShort(iso: string): string {
  const [y, m, d] = iso.split('-').map(Number)
  return `${WD[new Date(Date.UTC(y, m - 1, d, 12)).getUTCDay()]} ${pad(d)}.${pad(m)}.`
}

/** "Mi 28.10.2026" */
export function dayLong(iso: string): string {
  const [y, m, d] = iso.split('-').map(Number)
  return `${WD[new Date(Date.UTC(y, m - 1, d, 12)).getUTCDay()]} ${pad(d)}.${pad(m)}.${y}`
}

/** "HH:MM" → minutes of the day (null for nonsense). */
export function parseHHMM(s: string | undefined | null): number | null {
  const m = /^(\d{1,2}):(\d{2})$/.exec(s ?? '')
  return m ? Number(m[1]) * 60 + Number(m[2]) : null
}

/** Is the keyboard target an input field? (Shortcuts must not swallow typing.) */
export function isTypingTarget(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null
  if (!el || !el.tagName) return false
  return el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || el.isContentEditable
}

/** Error message without an "Error: " prefix. */
export function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/** Error from an AbortController (stale request) — never show. */
export function isAbort(e: unknown): boolean {
  return e instanceof DOMException && e.name === 'AbortError'
}
