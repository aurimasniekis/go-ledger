import { ApiError, NetworkError } from '@/lib/api'

/** Why a merchant change was not saved, in words: nothing is shown as saved until the server accepted it. */
export function merchantProblem(e: unknown): string {
  if (e instanceof NetworkError) return 'Not saved: there is no connection to the server. Nothing was changed.'
  if (e instanceof ApiError) {
    switch (e.code) {
      case 'version_conflict':
        return 'This merchant was changed elsewhere. It is reloaded: check it and try again. Nothing was changed.'
      case 'merchant_alias_taken':
        return 'Another merchant already has this name or alias, so nothing was changed. Merge the two merchants, or remove the alias from the other one first.'
      case 'merchant_archived':
        return 'This merchant is archived or merged, so nothing new can be attached to it. Unarchive it first (a merged merchant stays merged). Nothing was changed.'
      case 'merchant_in_use':
        return 'Decisions, assignments or merged merchants still refer to this merchant, so it cannot be deleted. Archive it to stop new use, or merge it into another. Nothing was changed.'
      case 'nothing_to_confirm':
        return 'The source did not identify a merchant for this transaction, so there is nothing to confirm. Correct it instead. Nothing was changed.'
      case 'review_inherited':
        return 'This decision was inherited from the pending transaction this booking replaced, so it can only be cleared there. Nothing was changed.'
      case 'forbidden':
        return 'Not saved: your token or role does not hold merchants:write. Nothing was changed.'
    }
    return e.message
  }
  return e instanceof Error ? e.message : 'Something went wrong. Nothing was changed.'
}

export const isConflict = (e: unknown) => e instanceof ApiError && e.code === 'version_conflict'
