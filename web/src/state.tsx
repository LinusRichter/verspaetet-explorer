// Central state: navigation (tab + station/line/run + day + direction
// filter), browser history (back/forward), knowledge time (scrub) and
// theme. Everything on demand — nothing is preloaded except /api/meta.
//
// Day model: the explorer is a historical dataset. nav.day is the ISO day
// or null (= "not resolved yet", fresh page load); once meta arrives it is
// normalized to the LATEST available day and written into the URL. There
// is no "today".
//
// Rules:
// - Navigation = new history entry (pushNav), context switch = replaceNav.
// - navRef/scrubRef are set INSIDE pushNav/replaceNav/setScrub so two
//   back-to-back transitions never compute with stale state.
// - The scrub jumps to the day's end-of-data point ("Tagesende") only on a
//   real day change (setDay/goRun with another day); back/forward restore
//   the stored knowledge time and are never overwritten.
// - settledScrub = knowledge time the fetches react to: 250 ms trailing
//   while dragging, immediate on release; null = not known yet (meta
//   missing) → no fetch.

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { Meta, Tab } from './types'
import { api } from './api'
import { errMsg, isAbort } from './lib'

export type LineDir = 'all' | 'in' | 'out'

export interface NavOnly {
  tab: Tab
  station: string | null
  line: string | null
  run: string | null
  runDep: number | null
  stopIdx: number          // IRIS stop index (1-based); 0 = none selected
  day: string | null        // ISO date (YYYY-MM-DD); null = latest (resolved via meta)
  lineDir: LineDir         // direction filter in the line view (only with station scope)
}

// scrub null = knowledge time unknown when the entry was written (meta
// missing) — on the way back, jump to meta.now instead of 0.
interface HistState extends NavOnly { scrub: number | null; depth: number }

/** Line info of the currently loaded line/run (for the breadcrumb — no extra fetch). */
export interface ViewLine { key: string; label: string; ctx?: string; run?: string; dep?: number }

export interface RunOpts { line?: string | null; day?: string; station?: string | null }

interface Ctx {
  meta: Meta | null
  metaError: string | null
  scrub: number
  settledScrub: number | null
  setScrub: (t: number, live?: boolean) => void
  settleScrub: () => void
  theme: 'dark' | 'light'
  toggleTheme: () => void
  nav: NavOnly
  goStation: (station: string, at?: number | null) => void
  goLine: (lineKey: string, station: string | null, at?: number | null) => void
  goRun: (runId: string, stopIdx?: number, dep?: number, opts?: RunOpts) => void
  setTab: (tab: Tab) => void
  setDay: (day: string) => void
  selectRun: (runId: string, stopIdx?: number, dep?: number) => void
  selectLineRun: (lineKey: string, runId: string, dep?: number) => void
  selectLineDir: (dir: LineDir) => void
  setNavLine: (lineKey: string) => void   // adopt the line of the loaded run (replaceState)
  viewLine: ViewLine | null
  setViewLine: (v: ViewLine | null) => void
  canGoBack: boolean
  back: () => void
}

const C = createContext<Ctx | null>(null)

export function useApp(): Ctx {
  const c = useContext(C)
  if (!c) throw new Error('useApp outside provider')
  return c
}

// ---------------------------------------------------------------------------
// URL encoding (deep links)
// ---------------------------------------------------------------------------

function urlFor(nav: NavOnly, scrub: number | null): string {
  const p = new URLSearchParams()
  p.set('tab', nav.tab)
  if (nav.station) p.set('station', nav.station)
  if (nav.line) p.set('line', nav.line)
  if (nav.run) p.set('run', nav.run)
  if (nav.stopIdx) p.set('stop', String(nav.stopIdx))
  if (nav.runDep != null) p.set('dep', String(nav.runDep))
  if (scrub != null) p.set('at', String(scrub))
  if (nav.day) p.set('day', nav.day)
  if (nav.lineDir !== 'all') p.set('dir', nav.lineDir)
  const qs = p.toString()
  return qs ? `/?${qs}` : '/'
}

function intParam(v: string | null): number | null {
  if (v == null || v === '') return null
  const n = Number(v)
  return Number.isFinite(n) ? Math.round(n) : null
}

function navFromLocation(): { nav: NavOnly; scrub: number | null } {
  const p = new URLSearchParams(window.location.search)
  const tab = (['board', 'journey', 'line', 'day'] as const).includes(p.get('tab') as Tab)
    ? (p.get('tab') as Tab) : 'board'
  const dir = p.get('dir')
  const day = p.get('day')
  return {
    nav: {
      tab,
      station: p.get('station'),
      line: p.get('line'),
      run: p.get('run'),
      runDep: intParam(p.get('dep')),
      stopIdx: Math.max(0, intParam(p.get('stop')) ?? 0),
      day: day && /^\d{4}-\d{2}-\d{2}$/.test(day) ? day : null,
      lineDir: dir === 'in' || dir === 'out' ? dir : 'all',
    },
    scrub: intParam(p.get('at')),
  }
}

function validState(s: unknown): s is HistState {
  const v = s as HistState | null
  return !!v && typeof v === 'object' && typeof v.tab === 'string' &&
    ['board', 'journey', 'line', 'day'].includes(v.tab)
}

function readTheme(): 'dark' | 'light' {
  try {
    return localStorage.getItem('verspaetet-theme') === 'light' ? 'light' : 'dark'
  } catch { return 'dark' }
}

// ---------------------------------------------------------------------------

export function AppProvider({ children }: { children: ReactNode }) {
  const init = useRef(navFromLocation())
  const [meta, setMeta] = useState<Meta | null>(null)
  const [metaError, setMetaError] = useState<string | null>(null)
  const [scrub, setScrubRaw] = useState<number>(init.current.scrub ?? 0)
  const [settledScrub, setSettled] = useState<number | null>(init.current.scrub)
  const [theme, setTheme] = useState<'dark' | 'light'>(readTheme)
  const [nav, setNav] = useState<NavOnly>(init.current.nav)
  const [depth, setDepth] = useState(0)
  const [viewLine, setViewLine] = useState<ViewLine | null>(null)

  const navRef = useRef(nav)
  const scrubRef = useRef<number | null>(init.current.scrub)
  const depthRef = useRef(0)
  // Jump the scrub to the day's end-of-data point on the next meta arrival?
  // (startup without ?at=, or a real day change). Back/forward set it to false.
  const resetOnMeta = useRef(init.current.scrub == null)
  const metaRef = useRef<{ day: string | null; m: Meta } | null>(null)
  // Day already normalized (latest) — guards a second replaceNav.
  const dayNormalized = useRef(false)

  const writeHistory = useCallback((mode: 'push' | 'replace', next: NavOnly) => {
    const s: HistState = { ...next, scrub: scrubRef.current, depth: depthRef.current }
    if (mode === 'push') window.history.pushState(s, '', urlFor(next, scrubRef.current))
    else window.history.replaceState(s, '', urlFor(next, scrubRef.current))
  }, [])

  // Adopt the knowledge time: scrubRef immediately, the fetch state
  // (settled) + URL either immediately or (live = dragging) 250 ms trailing.
  const settleT = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const settleScrub = useCallback(() => {
    clearTimeout(settleT.current)
    if (scrubRef.current == null) return
    setSettled(scrubRef.current)
    writeHistory('replace', navRef.current)
  }, [writeHistory])

  const setScrub = useCallback((t: number, live = false) => {
    scrubRef.current = t
    setScrubRaw(t)
    clearTimeout(settleT.current)
    if (live) settleT.current = setTimeout(settleScrub, 250)
    else settleScrub()
  }, [settleScrub])

  // Meta per day. null day = latest; after arrival the nav is normalized
  // to that day (explicit in URL and cache keys).
  useEffect(() => {
    const ac = new AbortController()
    api.meta(nav.day, ac.signal).then((m) => {
      if (ac.signal.aborted) return
      metaRef.current = { day: navRef.current.day, m }
      setMeta(m)
      setMetaError(null)
      if (navRef.current.day == null && !dayNormalized.current && m.days.length > 0) {
        dayNormalized.current = true
        const latest = m.days[m.days.length - 1]
        navRef.current = { ...navRef.current, day: latest }
        writeHistory('replace', navRef.current)
        setNav(navRef.current)
      }
      if (resetOnMeta.current) {
        resetOnMeta.current = false
        setScrub(m.now)
      }
    }).catch((e) => { if (!ac.signal.aborted && !isAbort(e)) setMetaError(errMsg(e)) })
    return () => ac.abort()
  }, [nav.day, writeHistory, setScrub])

  const toggleTheme = useCallback(() => {
    setTheme((th) => {
      const next = th === 'dark' ? 'light' : 'dark'
      try { localStorage.setItem('verspaetet-theme', next) } catch { /* storage blocked */ }
      document.documentElement.dataset.theme = next
      return next
    })
  }, [])

  // --- history sync -------------------------------------------------------

  const pushNav = useCallback((next: NavOnly) => {
    navRef.current = next
    depthRef.current += 1
    setDepth(depthRef.current)
    writeHistory('push', next)
    setNav(next)
  }, [writeHistory])

  const replaceNav = useCallback((next: NavOnly) => {
    navRef.current = next
    writeHistory('replace', next)
    setNav(next)
  }, [writeHistory])

  useEffect(() => {
    const h = (e: PopStateEvent) => {
      if (!validState(e.state)) return
      const { scrub: at, depth: d, ...rest } = e.state
      clearTimeout(settleT.current)
      resetOnMeta.current = false
      const sameDay = rest.day === navRef.current.day
      navRef.current = rest
      depthRef.current = typeof d === 'number' ? d : 0
      setDepth(depthRef.current)
      if (typeof at === 'number' && Number.isFinite(at)) {
        scrubRef.current = at
        setScrubRaw(at)
        setSettled(at)
      } else if (sameDay && metaRef.current && metaRef.current.day === rest.day) {
        setScrub(metaRef.current.m.now)
      } else {
        // knowledge time unknown → wait for the day's meta (no at=0 request).
        scrubRef.current = null
        setSettled(null)
        resetOnMeta.current = true
      }
      setNav(rest)
    }
    window.addEventListener('popstate', h)
    // Adopt the stored depth after a reload (otherwise "←" is wrongly disabled).
    const hs = window.history.state as HistState | null
    if (validState(hs) && typeof hs.depth === 'number' && hs.depth > 0) {
      depthRef.current = hs.depth
      setDepth(hs.depth)
    }
    writeHistory('replace', init.current.nav)
    return () => window.removeEventListener('popstate', h)
  }, [writeHistory, setScrub])

  // Day change as part of a transition: lock the scrub until the new meta.
  const withDay = useCallback((next: NavOnly, day: string | null): NavOnly => {
    if (day === navRef.current.day) return next
    resetOnMeta.current = true
    clearTimeout(settleT.current)
    setSettled(null)
    return { ...next, day, lineDir: 'all' }
  }, [])

  // Optional knowledge time in a transition (palette "Bahnhof 14:30"): set
  // BEFORE the push so the history entry carries the right at immediately.
  const applyAt = useCallback((at: number | null | undefined) => {
    if (at == null) return
    clearTimeout(settleT.current)
    resetOnMeta.current = false
    scrubRef.current = at
    setScrubRaw(at)
    setSettled(at)
  }, [])

  // --- navigation ---------------------------------------------------------

  const goStation = useCallback((station: string, at?: number | null) => {
    applyAt(at)
    pushNav({ ...navRef.current, tab: 'board', station })
  }, [pushNav, applyAt])

  const goLine = useCallback((lineKey: string, station: string | null, at?: number | null) => {
    applyAt(at)
    const cur = navRef.current
    pushNav({ ...cur, tab: 'line', line: lineKey, station, run: null, runDep: null, stopIdx: 0,
      lineDir: station === cur.station && lineKey === cur.line ? cur.lineDir : 'all' })
  }, [pushNav, applyAt])

  const goRun = useCallback((runId: string, stopIdx = 0, dep?: number, opts: RunOpts = {}) => {
    const cur = navRef.current
    let next: NavOnly = {
      ...cur, tab: 'journey', run: runId, runDep: dep ?? null, stopIdx,
      line: opts.line !== undefined ? opts.line : cur.line,
      station: opts.station !== undefined ? opts.station : cur.station,
    }
    if (opts.day !== undefined && opts.day !== cur.day) next = withDay(next, opts.day)
    pushNav(next)
  }, [pushNav, withDay])

  const setTab = useCallback((tab: Tab) => {
    if (navRef.current.tab === tab) return
    pushNav({ ...navRef.current, tab })
  }, [pushNav])

  const setDay = useCallback((day: string) => {
    if (day === navRef.current.day) return
    pushNav(withDay({ ...navRef.current }, day))
  }, [pushNav, withDay])

  const selectRun = useCallback((runId: string, stopIdx = 0, dep?: number) => {
    replaceNav({ ...navRef.current, run: runId, runDep: dep ?? null, stopIdx })
  }, [replaceNav])

  const selectLineRun = useCallback((lineKey: string, runId: string, dep?: number) => {
    replaceNav({ ...navRef.current, line: lineKey, run: runId, runDep: dep ?? null, stopIdx: 0 })
  }, [replaceNav])

  const selectLineDir = useCallback((dir: LineDir) => {
    replaceNav({ ...navRef.current, lineDir: dir })
  }, [replaceNav])

  const setNavLine = useCallback((lineKey: string) => {
    if (navRef.current.line === lineKey) return
    replaceNav({ ...navRef.current, line: lineKey })
  }, [replaceNav])

  const back = useCallback(() => window.history.back(), [])

  const value = useMemo<Ctx>(() => ({
    meta, metaError, scrub, settledScrub, setScrub, settleScrub, theme, toggleTheme,
    nav, goStation, goLine, goRun, setTab, setDay, selectRun, selectLineRun, selectLineDir, setNavLine,
    viewLine, setViewLine,
    canGoBack: depth > 0, back,
  }), [meta, metaError, scrub, settledScrub, setScrub, settleScrub, theme, toggleTheme, nav,
    goStation, goLine, goRun, setTab, setDay, selectRun, selectLineRun, selectLineDir, setNavLine,
    viewLine, depth, back])

  return <C.Provider value={value}>{children}</C.Provider>
}
