// Chart colors per theme. The UI colors (text, panels, chips) live as CSS
// variables in index.css — SVG needs concrete hex values, hence this file.

export interface Palette {
  grid: string
  axis: string
  label: string
  poly: string
  polySel: string
  now: string
  ok: string
  warn: string
  orange: string
  bad: string
}

const DARK: Palette = {
  grid: '#2a2a2a', axis: '#555', label: '#8a8a8a', poly: '#5a5a5a', polySel: '#4a9eff',
  now: '#ff5a5a', ok: '#5ec46b', warn: '#e8c34a', orange: '#e8874a', bad: '#ff5a5a',
}

const LIGHT: Palette = {
  grid: '#e7e7e1', axis: '#b5b5ae', label: '#777', poly: '#c2c2ba', polySel: '#0a62c2',
  now: '#cc2f2f', ok: '#1a8f3c', warn: '#9a7414', orange: '#c2621e', bad: '#cc2f2f',
}

export function palette(theme: 'dark' | 'light'): Palette {
  return theme === 'dark' ? DARK : LIGHT
}

export function delayHex(p: Palette, d: number | null): string {
  if (d == null) return p.label
  if (d >= 10) return p.bad
  if (d >= 6) return p.orange
  if (d >= 3) return p.warn
  return p.ok
}

/** Line color by category — deterministic, same color as the board dot. */
const CAT_COLORS: Record<string, string> = {
  ICE: '#d64545', IC: '#d64545', RE: '#2aa198', RB: '#7f9db9', S: '#5ec46b',
}
export function catColor(cat: string, p: Palette): string {
  return CAT_COLORS[cat.toUpperCase()] ?? p.poly
}
