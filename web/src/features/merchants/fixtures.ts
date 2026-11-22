import type { MerchantAlias, MerchantRecord, TransactionMerchant } from './types'

const now = '2026-11-21T10:00:00Z'
export const mid = (n: string) => '01JD3Y4C8V9M2Q7W5R6T0XM' + n.padEnd(3, '0').slice(0, 3)

export const alias = (n: string, name: string, over: Partial<MerchantAlias> = {}): MerchantAlias => ({ id: mid('A' + n), name, key: name.toLowerCase(), origin: 'user', created_at: now, ...over })

export function merchant(n: string, name: string, over: Partial<MerchantRecord> = {}): MerchantRecord {
  return { id: mid(n), display_name: name, aliases: [alias(n + '1', name)], archived: false, archived_at: null, merged_into: null, version: 1, created_at: now, updated_at: now, ...over }
}

export function txMerchant(over: Partial<TransactionMerchant> = {}): TransactionMerchant {
  return {
    transaction_id: 'tx-1',
    state: 'none',
    merchant_id: null,
    name: null,
    logo_ref: null,
    identity_source: null,
    fallback: true,
    needs_review: false,
    confirmation_outdated: false,
    review_inherited: false,
    enrichment: null,
    review: null,
    ...over,
  }
}
