// One fetch pattern for all views: an AbortController per key, errors only
// for the current key, and data is shown only while it belongs to the
// current CONTEXT (e.g. board: station+day). The old station's board thus
// never stands under the new name; a pure scrub change (same context)
// keeps the old rows until the new ones arrive (no flicker).
// 503 (server busy) is retried automatically after Retry-After (max. 3×).

import { useEffect, useRef, useState } from 'react'
import { errMsg, isAbort } from './lib'
import { ApiError } from './api'

interface St<T> { key: string; ctx: string; data?: T; err?: string }

export function useFetch<T>(
  key: string | null,
  fn: (signal: AbortSignal) => Promise<T>,
  ctx: string | null = key,
): { data: T | null; err: string | null; loading: boolean } {
  const [st, setSt] = useState<St<T> | null>(null)
  const [attempt, setAttempt] = useState<{ key: string | null; n: number }>({ key, n: 0 })
  const n = attempt.key === key ? attempt.n : 0
  const fnRef = useRef(fn)
  fnRef.current = fn
  const ctxRef = useRef(ctx)
  ctxRef.current = ctx

  useEffect(() => {
    if (key == null) return
    const ac = new AbortController()
    const c = ctxRef.current ?? key
    let retryT: ReturnType<typeof setTimeout> | undefined
    fnRef.current(ac.signal).then(
      (d) => { if (!ac.signal.aborted) setSt({ key, ctx: c, data: d }) },
      (e) => {
        if (ac.signal.aborted || isAbort(e)) return
        setSt((prev) => ({ key, ctx: c, data: prev?.ctx === c ? prev.data : undefined, err: errMsg(e) }))
        if (e instanceof ApiError && e.status === 503 && n < 3) {
          retryT = setTimeout(() => setAttempt({ key, n: n + 1 }), (e.retryAfter ?? 10) * 1000)
        }
      },
    )
    return () => { ac.abort(); clearTimeout(retryT) }
  }, [key, n])

  const c = ctx ?? key
  const data = st && st.data !== undefined && st.ctx === c ? st.data : null
  const err = st && st.err && st.key === key ? st.err : null
  const loading = key != null && (!st || st.key !== key)
  return { data, err, loading }
}
