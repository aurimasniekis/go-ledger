import { useCallback, useEffect, useRef } from 'react'
import { useMutation, useQueryClient, type QueryKey } from '@tanstack/react-query'
import { useAnnounce } from '@/components/announcer'
import { ApiError } from '@/lib/api'

/**
 * Where a save stands. "pending" means sent and not confirmed: the UI must call it "Not saved yet"
 * (StatusBadge status="pending") and never show it as saved. "failed" means the server refused or was
 * unreachable and everything shown for it has been rolled back. "saved" means the server confirmed
 * and the cached data was reconciled with the server's answer.
 */
export type SavePhase = 'idle' | 'pending' | 'failed' | 'saved'

export interface OptimisticSaveOptions<TVars, TServer, TCache> {
  /** Cached queries this save changes; they are refetched after a confirmed save, so the server is the truth. */
  queryKey: QueryKey
  mutationFn: (vars: TVars) => Promise<TServer>
  /**
   * Optional: show the change in the cache at once. Leave it out for money-facing changes (a recorded
   * payment): the hook then only reports "pending" and nothing is shown as changed before the server confirms.
   * When given, the previous cache is restored on failure.
   */
  apply?: (cache: TCache | undefined, vars: TVars) => TCache
  /** Optional: write the server's answer into the cache (instead of waiting for the refetch). */
  reconcile?: (cache: TCache | undefined, server: TServer, vars: TVars) => TCache
  /** Runs after the server confirmed and the cache was reconciled. */
  onSaved?: (server: TServer, vars: TVars) => void
  /**
   * Optional: after a version conflict (409/412), the variables to resend, rebuilt on the server's current
   * version from the error's reconciliation data. Without it, or when it returns undefined, retry() does
   * nothing after a conflict: a stale version is never resent.
   */
  rebase?: (vars: TVars, conflict: ApiError) => TVars | undefined
  /** Announced (without moving focus) at each outcome. A failed message of "" announces nothing (the form already shows it). */
  messages?: { saved?: string | ((server: TServer) => string); failed?: (error: Error) => string }
}

/** A refusal because the saved object changed since it was read (409 or 412 with version_conflict, or any 412). */
export const isVersionConflict = (e: unknown): e is ApiError =>
  e instanceof ApiError && (e.status === 412 || (e.status === 409 && e.code === 'version_conflict'))

/**
 * The shared optimistic-save primitive (U1). One save at a time: save() while one is in flight is ignored,
 * even within the same tick (a synchronous in-flight flag, not the rendered pending state).
 * A failed save keeps its variables so retry() resends exactly what the user asked for, except after a
 * version conflict, where the cache is refetched and retry() only resends variables rebuilt by `rebase`;
 * discard() drops it. Every failure invalidates the affected queries. Failure and the confirmed save are
 * announced through the live region (pending is shown by the caller as "Not saved yet", not announced).
 */
export function useOptimisticSave<TVars, TServer = unknown, TCache = unknown>(o: OptimisticSaveOptions<TVars, TServer, TCache>) {
  const qc = useQueryClient()
  const announce = useAnnounce()
  const last = useRef<TVars | undefined>(undefined)
  const inFlight = useRef(false)
  const opts = useRef(o)
  useEffect(() => {
    opts.current = o
  })

  const m = useMutation<TServer, Error, TVars, { previous: TCache | undefined; applied: boolean }>({
    mutationFn: (vars) => opts.current.mutationFn(vars),
    onMutate: async (vars) => {
      const { queryKey, apply } = opts.current
      if (!apply) return { previous: undefined, applied: false }
      await qc.cancelQueries({ queryKey })
      const previous = qc.getQueryData<TCache>(queryKey)
      qc.setQueryData<TCache>(queryKey, apply(previous, vars))
      return { previous, applied: true }
    },
    onError: (error, _vars, ctx) => {
      const { queryKey, messages } = opts.current
      if (ctx?.applied) qc.setQueryData(queryKey, ctx.previous)
      // Whatever went wrong, what is cached may no longer be what the server holds: fetch it again.
      void qc.invalidateQueries({ queryKey })
      const text = messages?.failed?.(error) ?? (isVersionConflict(error) ? 'Not saved: this changed elsewhere. The latest version has been loaded; review it and save again.' : `Not saved: ${error.message}`)
      if (text) announce(text, 'assertive')
    },
    onSettled: () => {
      inFlight.current = false
    },
    onSuccess: async (server, vars) => {
      const { queryKey, reconcile, onSaved, messages } = opts.current
      if (reconcile) qc.setQueryData<TCache>(queryKey, (c) => reconcile(c, server, vars))
      // The server is the truth: refetch, and stay "pending" until that has happened.
      await qc.invalidateQueries({ queryKey })
      const saved = messages?.saved
      announce(typeof saved === 'function' ? saved(server) : (saved ?? 'Saved.'))
      onSaved?.(server, vars)
    },
  })

  const { mutate } = m
  const save = useCallback(
    (vars: TVars) => {
      if (inFlight.current) return
      inFlight.current = true
      last.current = vars
      mutate(vars)
    },
    [mutate],
  )
  const conflict = isVersionConflict(m.error) ? m.error : undefined
  const retry = useCallback(() => {
    if (last.current === undefined) return
    if (conflict) {
      const next = opts.current.rebase?.(last.current, conflict)
      if (next !== undefined) save(next)
      return
    }
    save(last.current)
  }, [save, conflict])

  const phase: SavePhase = m.isPending ? 'pending' : m.isError ? 'failed' : m.isSuccess ? 'saved' : 'idle'
  return {
    phase,
    /** What is being saved while pending or failed; undefined otherwise. */
    vars: m.isPending || m.isError ? m.variables : undefined,
    error: m.error,
    /** Set while the failure is a version conflict: the server has a newer version; the cache is being refetched. */
    conflict,
    result: m.data,
    save,
    retry,
    discard: m.reset,
  }
}
