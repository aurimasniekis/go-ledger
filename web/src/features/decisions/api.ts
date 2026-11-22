import { apiFetch } from '@/lib/api'
import type { Alert, Suggestion } from './types'

/** Every cached query of this screen starts with this key, so one invalidation reloads them all. */
export const decisionsKey = ['decisions'] as const

const enc = encodeURIComponent

/** accept, reject and revert carry no body; a state moves once, so there is no If-Match (a second action is a 409). */
export const suggestionAction = (id: string, action: 'accept' | 'reject' | 'revert') =>
  apiFetch<Suggestion>(`/api/v1/suggestions/${enc(id)}/${action}`, { method: 'POST', json: {} })

/** The person's own answer instead: a category id (null: no category) or label ids. */
export const correctSuggestion = (id: string, answer: { categoryId: string | null } | { labelIds: string[] }) =>
  apiFetch<Suggestion>(`/api/v1/suggestions/${enc(id)}/correct`, {
    method: 'POST',
    json: 'labelIds' in answer ? { label_ids: answer.labelIds } : { category_id: answer.categoryId },
  })

export const dismissAlert = (id: string) => apiFetch<Alert>(`/api/v1/alerts/${enc(id)}/dismiss`, { method: 'POST', json: {} })

/** What a refused action means for the person (ADR-0067 point 6). */
export const ACTION_ERRORS: Record<string, string> = {
  suggestion_not_pending: 'This suggestion was already decided, here or elsewhere. Nothing was changed by this action; the list has been reloaded.',
  not_reversible: 'This cannot be reverted: it was not applied automatically, was already reverted, or the category was changed since. Nothing was changed.',
  assignment_changed: 'The category or labels of this object changed since the suggestion was made. Nothing was changed; review the object first.',
  nothing_to_apply: 'The model chose nothing to apply, so there is nothing to accept. You can correct it with your own answer or reject it.',
  wrong_task: 'This suggestion is of a different kind than the action expects. Nothing was changed.',
  definition_archived: 'That category or label is archived, so it cannot be assigned. The suggestion stays pending; correct it with an active one or reject it.',
  alert_not_open: 'This alert was already closed. The list has been reloaded.',
  forbidden: 'You do not hold the grants this action needs. Nothing was changed.',
}
