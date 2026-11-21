import { useCallback, useEffect, useRef } from 'react'
import { useMutation, useQueryClient, type QueryKey } from '@tanstack/react-query'
import { useAnnounce } from '@/components/announcer'

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
  /** Announced (without moving focus) at each outcome. */
  messages?: { saved?: string | ((server: TServer) => string); failed?: (error: Error) => string }
}

/**
 * The shared optimistic-save primitive (U1). One save at a time: save() while pending is ignored.
 * A failed save keeps its variables so retry() resends exactly what the user asked for; discard()
 * drops it. Pending, failure and reconciliation are announced through the live region.
 */
export function useOptimisticSave<TVars, TServer = unknown, TCache = unknown>(o: OptimisticSaveOptions<TVars, TServer, TCache>) {
  const qc = useQueryClient()
  const announce = useAnnounce()
  const last = useRef<TVars | undefined>(undefined)
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
      announce(messages?.failed?.(error) ?? `Not saved: ${error.message}`, 'assertive')
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

  const { isPending, mutate } = m
  const save = useCallback(
    (vars: TVars) => {
      if (isPending) return
      last.current = vars
      mutate(vars)
    },
    [isPending, mutate],
  )
  const retry = useCallback(() => {
    if (last.current !== undefined) save(last.current)
  }, [save])

  const phase: SavePhase = m.isPending ? 'pending' : m.isError ? 'failed' : m.isSuccess ? 'saved' : 'idle'
  return {
    phase,
    /** What is being saved while pending or failed; undefined otherwise. */
    vars: m.isPending || m.isError ? m.variables : undefined,
    error: m.error,
    result: m.data,
    save,
    retry,
    discard: m.reset,
  }
}
