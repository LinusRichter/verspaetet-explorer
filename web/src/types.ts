// Domain types of the data UI. Times are minutes of the day (Europe/Berlin)
// wherever they are time points — matching the API endpoints.

export type Tab = 'board' | 'journey' | 'line' | 'day'

export interface Meta {
  day: string       // selected day, ISO YYYY-MM-DD
  days: string[]    // all available days, oldest first
  t0: number        // timeline start (min)
  t1: number        // timeline end (min)
  now: number       // end of the day's data (= t1): the slider's "final knowledge" point
}

export interface LineInfo {
  key: string       // unique line identity (e.g. cat|label)
  label: string     // public label ("RE 7", "S 6")
  cat: string       // category (ICE, RE, RB, S, …)
  ctx?: string      // area context for ambiguous labels
}

export interface Snap {
  at: number        // knowledge time (min)
  prog: number | null // delay in min; null = no forecast
  ist: boolean      // actually happened
  cancelled: boolean
  plat: string
}

export interface Stop {
  name: string
  eva: string
  idx: number       // IRIS stop index, 1-based (like BoardRow.stopIdx / SearchTrip.stopIdx)
  planned: number
  finalD: number    // last known delay
  actual: number    // planned + finalD
  cancelled: boolean
  platFrom: string  // planned platform
  platTo: string    // last known platform
  platKnown: number // from when a platform change was known (min; 1e9 = no change)
  snaps: Snap[]
}

export interface Run {
  id: string        // trip key (stop_id prefix)
  lineKey: string
  dep: number       // departure time (min)
  cancelled: boolean
  stops: Stop[]
}

export interface SearchStation { name: string; n: number } // n = runs stopping here that day (0 = unknown)
export interface SearchLine extends LineInfo { n: number; ctx: string }
export interface LineAtStation {
  line: LineInfo
  station: string
}

export interface SearchResults {
  stations: SearchStation[]
  lines: SearchLine[]
  time?: number | null          // time detected in the query (minutes)
  stationName?: string          // strongest station hit …
  stationLines?: SearchLine[]   // … and the lines stopping there
  lineAtStation?: LineAtStation[] // "horrem s12" → line at station
  trips?: SearchTrip[]          // individual runs (train number / line+station+time)
  pending?: boolean             // day index still building — lines/runs may be incomplete
}

/** Individual run from /api/search. */
export interface SearchTrip {
  runId: string
  day: string       // YYYY-MM-DD (Berlin) — passed as &day= to /api/journey
  key: string       // line key
  label: string     // "ICE 579" / "S 12"
  cat: string
  number: string    // train number
  from: string
  to: string
  dep: string       // "HH:MM" (at station, else at from)
  station?: string  // station where the match happened
  stopIdx?: number  // IRIS stop index (1-based) at station
}

export interface BoardRow {
  runId: string
  stopIdx: number   // IRIS stop index, 1-based
  lineKey: string
  lineLabel: string
  planned: number
  known: boolean
  delay: number | null
  ist: boolean
  cancelled: boolean
  plat: string
  platFrom: string
  platChanged: boolean
  dest: string
  via: string
}

export interface BoardData {
  rows: BoardRow[]
  lines: LineInfo[]    // lines stopping at this station
}

export interface LineRunStop { name: string; finalD: number; cancelled: boolean }
export interface LineRun { id: string; dep: number; cancelled: boolean; finalD: number; stops: LineRunStop[] }

export interface AltLine {
  key: string
  label: string
  cat: string
  runId: string   // most relevant run for direct jumps
  dep: number
}

export interface JourneyData {
  run: Run
  line: LineInfo & { evu: string }
  altLines: AltLine[]
  altScoped: boolean // true = scoped to the station context
}

export interface LineData {
  line: LineInfo
  ctx?: string       // disambiguation (e.g. "Hagen Hbf ⇢ Köln Hbf")
  runs: LineRun[]
  stats: { n: number; cx: number; p50: number; p90: number; max: number; punc: number; gc: number }
  altLines: AltLine[]
  altScoped: boolean
}

export interface DayKpis { runs: number; punc: number; avg: number; cx: number; gc: number; snaps: number }
export interface HourBar { h: number; avg: number; n: number }
export interface LeaderRow { key: string; label: string; n: number; p50: number; p90: number; max: number; punc: number; cx: number }
export interface TopRow { runId: string; label: string; dep: number; from: string; to: string; finalD: number }
export interface DayData {
  kpis: DayKpis
  hours: HourBar[]
  leaderboard: LeaderRow[]
  top: TopRow[]
}
