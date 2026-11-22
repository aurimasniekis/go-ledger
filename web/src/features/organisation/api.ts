import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { usePagedList } from '@/lib/paged'
import { KIND_PATH, type BucketMember, type Definition, type Kind, type ObjectAssignments, type ObjectType } from './types'

/** Every cached query of the organisation screens starts with this key, so one invalidation reloads them all. */
export const orgKey = ['organisation'] as const

const base = (k: Kind) => `/api/v1/${KIND_PATH[k]}`
const enc = encodeURIComponent
const ifMatch = (d: Definition) => ({ 'If-Match': `"${d.version}"` })

export interface DefinitionFilter {
  archived?: 'true' | 'false'
  prefix?: string
}

/** One kind of definition (organisation:read), cursor-paged and filtered by the server. */
export function useDefinitions(kind: Kind, f: DefinitionFilter = {}, enabled = true) {
  return usePagedList<Definition>([...orgKey, 'definitions', kind], base(kind), { archived: f.archived, 'name[prefix]': f.prefix }, { enabled })
}

/** Reads every remaining page of a paged query: definitions are few, and a picker or a tree needs all of them. */
export function useLoadAll(q: { hasNextPage: boolean; isFetchingNextPage: boolean; isFetchNextPageError: boolean; fetchNextPage: () => unknown }) {
  const { hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage } = q
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage && !isFetchNextPageError) void fetchNextPage()
  }, [hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage])
}

/** Every definition of a kind, active ones only by default; `complete` is false until the last page has loaded. */
export function useAllDefinitions(kind: Kind, archived?: 'true' | 'false', enabled = true) {
  const q = useDefinitions(kind, { archived }, enabled)
  useLoadAll(q)
  return { query: q, items: q.data ?? [], complete: q.data !== undefined && !q.hasNextPage }
}

export const createDefinition = (k: Kind, body: { name: string; parent_id?: string | null }) => apiFetch<Definition>(base(k), { method: 'POST', json: body })
export const patchDefinition = (k: Kind, d: Definition, patch: { name?: string; parent_id?: string | null }) =>
  apiFetch<Definition>(`${base(k)}/${enc(d.id)}`, { method: 'PATCH', headers: ifMatch(d), json: patch })
export const archiveDefinition = (k: Kind, d: Definition, archive: boolean) =>
  apiFetch<Definition>(`${base(k)}/${enc(d.id)}/${archive ? 'archive' : 'unarchive'}`, { method: 'POST', headers: ifMatch(d), json: {} })
export const deleteDefinition = (k: Kind, d: Definition, reassignTo?: string) =>
  apiFetch<{ reassigned: number }>(`${base(k)}/${enc(d.id)}${reassignTo ? `?reassign_to=${enc(reassignTo)}` : ''}`, { method: 'DELETE', headers: ifMatch(d) })

/** The commitments and accounts in a bucket. An archived bucket keeps its members. */
export function useBucketMembers(bucketId: string | undefined, enabled = true) {
  return usePagedList<BucketMember>([...orgKey, 'members', bucketId ?? ''], `${base('bucket')}/${enc(bucketId ?? '')}/members`, {}, { enabled: enabled && !!bucketId })
}
export const addBucketMember = (bucketId: string, type: 'commitment' | 'account', id: string) =>
  apiFetch<void>(`${base('bucket')}/${enc(bucketId)}/members/${type}/${enc(id)}`, { method: 'PUT' })
export const removeBucketMember = (bucketId: string, type: 'commitment' | 'account', id: string) =>
  apiFetch<void>(`${base('bucket')}/${enc(bucketId)}/members/${type}/${enc(id)}`, { method: 'DELETE' })

const obj = (t: ObjectType, id: string) => `/api/v1/organisation/${t}/${enc(id)}`
export const objectKey = (t: ObjectType, id: string) => [...orgKey, 'object', t, id] as const

/** What is assigned to one object. Read only when asked (enabled), so a long list does not fetch one request per row. */
export function useObjectOrganisation(t: ObjectType, id: string, enabled: boolean) {
  return useQuery({ queryKey: objectKey(t, id), enabled, queryFn: ({ signal }) => apiFetch<ObjectAssignments>(obj(t, id), { signal }) })
}
export const setObjectCategory = (t: ObjectType, id: string, categoryId: string | null) =>
  apiFetch<ObjectAssignments>(`${obj(t, id)}/category`, { method: 'PUT', json: { category_id: categoryId } })
export const resetObjectCategory = (t: ObjectType, id: string) => apiFetch<ObjectAssignments>(`${obj(t, id)}/category`, { method: 'DELETE' })
export const addObjectLabel = (t: ObjectType, id: string, labelId: string) => apiFetch<ObjectAssignments>(`${obj(t, id)}/labels/${enc(labelId)}`, { method: 'PUT' })
export const removeObjectLabel = (t: ObjectType, id: string, labelId: string) => apiFetch<ObjectAssignments>(`${obj(t, id)}/labels/${enc(labelId)}`, { method: 'DELETE' })
