import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { usePagedList } from '@/lib/paged'
import type { Decision, MergeResult, MerchantRecord, MerchantSource, TransactionMerchant } from './types'

/** Every cached query of the merchant screens starts with this key, so one invalidation reloads them all. */
export const merchantsKey = ['merchants'] as const
export const merchantKey = (id: string) => [...merchantsKey, 'item', id] as const
export const txMerchantKey = (txId: string) => [...merchantsKey, 'transaction', txId] as const

const base = '/api/v1/merchants'
const enc = encodeURIComponent
const ifMatch = (m: Pick<MerchantRecord, 'version'>) => ({ 'If-Match': `"${m.version}"` })

/** Merchant records, cursor-paged. Archived ones (and merged ones, which are archived) only when asked. */
export function useMerchants(includeArchived: boolean, enabled = true) {
  return usePagedList<MerchantRecord>([...merchantsKey, 'list'], base, { archived: includeArchived ? 'true' : undefined }, { enabled })
}

export function useMerchant(id: string, enabled = true) {
  return useQuery({ queryKey: merchantKey(id), enabled, queryFn: ({ signal }) => apiFetch<MerchantRecord>(`${base}/${enc(id)}`, { signal }) })
}

export const createMerchant = (body: { display_name: string; aliases?: string[] }) => apiFetch<MerchantRecord>(base, { method: 'POST', json: body })
export const renameMerchant = (m: MerchantRecord, displayName: string) => apiFetch<MerchantRecord>(`${base}/${enc(m.id)}`, { method: 'PATCH', headers: ifMatch(m), json: { display_name: displayName } })
export const archiveMerchant = (m: MerchantRecord, archive: boolean) =>
  apiFetch<MerchantRecord>(`${base}/${enc(m.id)}/${archive ? 'archive' : 'unarchive'}`, { method: 'POST', headers: ifMatch(m), json: {} })
export const deleteMerchant = (m: MerchantRecord) => apiFetch<void>(`${base}/${enc(m.id)}`, { method: 'DELETE', headers: ifMatch(m) })
export const addAlias = (m: MerchantRecord, name: string) => apiFetch<MerchantRecord>(`${base}/${enc(m.id)}/aliases`, { method: 'POST', json: { name } })
export const removeAlias = (m: MerchantRecord, aliasId: string) => apiFetch<MerchantRecord>(`${base}/${enc(m.id)}/aliases/${enc(aliasId)}`, { method: 'DELETE' })
/** Merges `fromId` into `into` (the remaining merchant, whose version is the If-Match). */
export const mergeMerchants = (into: MerchantRecord, fromId: string) => apiFetch<MergeResult>(`${base}/${enc(into.id)}/merge`, { method: 'POST', headers: ifMatch(into), json: { merchant_id: fromId } })

/** The merchant of a bank transaction, read only when asked (enabled): a long list must not fetch one request per row. */
export function useTransactionMerchant(txId: string, enabled: boolean) {
  return useQuery({ queryKey: txMerchantKey(txId), enabled, queryFn: ({ signal }) => apiFetch<TransactionMerchant>(`/api/v1/transactions/${enc(txId)}/merchant`, { signal }) })
}
export const reviewTransactionMerchant = (txId: string, body: { decision: Decision; name?: string; merchant_id?: string }) =>
  apiFetch<TransactionMerchant>(`/api/v1/transactions/${enc(txId)}/merchant/review`, { method: 'PUT', json: body })
export const clearTransactionMerchantReview = (txId: string) => apiFetch<TransactionMerchant>(`/api/v1/transactions/${enc(txId)}/merchant/review`, { method: 'DELETE' })

/** What each identification source is and the attribution its licence asks to be shown with its answers. */
export function useMerchantSources(enabled = true) {
  return useQuery({ queryKey: [...merchantsKey, 'sources'], enabled, staleTime: 60_000, queryFn: async () => (await apiFetch<{ sources: MerchantSource[] }>(`${base}/sources`)).sources })
}
