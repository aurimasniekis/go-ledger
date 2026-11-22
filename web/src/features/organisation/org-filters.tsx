import { useMemo, useState, type ReactNode } from 'react'
import { Select } from '@/components/ui/input'
import { Field } from '@/components/field'
import { holds, useMe } from '@/lib/auth'
import { useAllDefinitions } from './api'

export interface OrgFilters {
  /** The two selects; null without the organisation:read grant or when no category and no label exists. */
  control: ReactNode
  /** The query parameters to send: category_id and label_id, each only when chosen. */
  params: { category_id?: string; label_id?: string }
  /** Whether a filter is chosen. */
  active: boolean
  /** The chosen filter in words, such as "category Housing and label Shared" (empty when none). */
  describe: string
}

/**
 * Category and label filters for a list of organised objects (task O3). A category also matches its child categories.
 * The server applies the filter, so a paged list stays complete and a filtered month's totals cover only what matches.
 * The filter needs organisation:read; without it nothing is shown and nothing is sent.
 */
export function useOrgFilters(subject: string): OrgFilters {
  const canRead = holds(useMe().data?.grants, 'organisation:read')
  const categories = useAllDefinitions('category', undefined, canRead)
  const labels = useAllDefinitions('label', undefined, canRead)
  const [categoryId, setCategoryId] = useState('')
  const [labelId, setLabelId] = useState('')
  const byId = useMemo(() => new Map(categories.items.map((c) => [c.id, c])), [categories.items])
  const nameOf = (id: string) => {
    const c = byId.get(id)
    if (!c) return 'category'
    const parent = c.parent_id ? byId.get(c.parent_id) : undefined
    return (parent ? parent.name + ' / ' : '') + c.name + (c.archived ? ' (archived)' : '')
  }
  const params = { ...(categoryId ? { category_id: categoryId } : {}), ...(labelId ? { label_id: labelId } : {}) }
  const describe = [categoryId ? 'category ' + nameOf(categoryId) : '', labelId ? 'label ' + (labels.items.find((l) => l.id === labelId)?.name ?? '') : ''].filter(Boolean).join(' and ')
  if (!canRead || (categories.items.length === 0 && labels.items.length === 0)) return { control: null, params: {}, active: false, describe: '' }
  const control = (
    <div className="flex flex-wrap items-end gap-3">
      <Field label={'Show only ' + subject + ' in category'} name="category-filter" className="min-w-48 max-w-xs">
        {(p) => (
          <Select {...p} value={categoryId} onChange={(e) => setCategoryId(e.target.value)}>
            <option value="">Any category</option>
            {categories.items.map((c) => (
              <option key={c.id} value={c.id}>
                {nameOf(c.id)}
              </option>
            ))}
          </Select>
        )}
      </Field>
      <Field label={'Show only ' + subject + ' with label'} name="label-filter" className="min-w-48 max-w-xs">
        {(p) => (
          <Select {...p} value={labelId} onChange={(e) => setLabelId(e.target.value)}>
            <option value="">Any label</option>
            {labels.items.map((l) => (
              <option key={l.id} value={l.id}>
                {l.name}
                {l.archived ? ' (archived)' : ''}
              </option>
            ))}
          </Select>
        )}
      </Field>
    </div>
  )
  return { control, params, active: categoryId !== '' || labelId !== '', describe }
}
