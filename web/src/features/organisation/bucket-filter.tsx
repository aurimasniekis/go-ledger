import { useMemo, useState, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/input'
import { Field } from '@/components/field'
import { holds, useMe } from '@/lib/auth'
import { useAllDefinitions, useBucketMembers, useLoadAll } from './api'

/**
 * A bucket filter for a list of commitments (the monthly view). A bucket is a grouping and holds no money, so the filter
 * only narrows the rows: the totals of the page keep covering everything, and the note says so. While the members load,
 * no row is shown rather than every row, and a failed load says so and shows every row.
 */
export function useBucketFilter(): {
  control: ReactNode
  visible?: (item: { commitment_id: string }) => boolean
  note?: (shown: number, total: number) => ReactNode
} {
  const me = useMe()
  const canRead = holds(me.data?.grants, 'organisation:read')
  const buckets = useAllDefinitions('bucket', undefined, canRead)
  const [bucketId, setBucketId] = useState('')
  const members = useBucketMembers(bucketId || undefined, canRead)
  useLoadAll(members)
  const ids = useMemo(() => (members.data && !members.hasNextPage ? new Set(members.data.filter((m) => m.object_type === 'commitment').map((m) => m.object_id)) : undefined), [members.data, members.hasNextPage])
  const bucket = buckets.items.find((b) => b.id === bucketId)
  if (!canRead || buckets.items.length === 0) return { control: null }
  const control = (
    <Field label="Show only commitments in bucket" name="bucket-filter" className="min-w-48 max-w-xs">
      {(p) => (
        <Select {...p} value={bucketId} onChange={(e) => setBucketId(e.target.value)}>
          <option value="">All commitments</option>
          {buckets.items.map((b) => (
            <option key={b.id} value={b.id}>
              {b.name}
              {b.archived ? ' (archived)' : ''}
            </option>
          ))}
        </Select>
      )}
    </Field>
  )
  if (!bucketId) return { control }
  // A failure on the first page, or on a later one (the first page alone would hide the rest): the filter cannot be applied.
  if ((members.isError && !members.data) || members.isFetchNextPageError) {
    return {
      control,
      note: () => (
        <p role="alert" className="text-sm text-status-error">
          The members of {bucket?.name ?? 'the bucket'} could not be loaded, so the filter is not applied and every occurrence is shown.{' '}
          <Button variant="outline" size="sm" onClick={() => void (members.data ? members.fetchNextPage() : members.refetch())}>
            Try again
          </Button>
        </p>
      ),
    }
  }
  return {
    control,
    visible: (i) => ids?.has(i.commitment_id) ?? false,
    note: (shown, total) => (
      <p role="status" className="text-sm text-muted-foreground">
        {ids ? `Showing ${shown} of ${total} occurrences: those of commitments in ${bucket?.name ?? 'the bucket'}.` : `Loading the members of ${bucket?.name ?? 'the bucket'}…`} The totals above still cover the whole month.
      </p>
    ),
  }
}
