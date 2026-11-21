// The server for the offline main journey (U3): the real ledger binary, started with a throwaway database, next to a
// simulated Enable Banking API on loopback. Nothing here reaches the network, and no real credentials exist: the
// application key is generated on every start, the bank is invented, the password is a fixed test value.
//
// Usage (playwright.config.ts starts it): node journey-server.mjs <ledger-binary>
// The simulated bank also has a control surface the spec drives (POST /__control/<name>), so the journey can show a
// bank outage and a withdrawn consent without a live bank.

import { spawn, spawnSync } from 'node:child_process'
import { generateKeyPairSync } from 'node:crypto'
import { mkdtempSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { BANK_PORT, JOURNEY_PASSWORD, JOURNEY_USER, LEDGER_PORT, journeyDates } from './journey-data.mjs'


const bin = process.argv[2] ?? '../bin/ledger'
const dir = mkdtempSync(join(tmpdir(), 'ledger-journey-'))
const { privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
const keyPem = privateKey.export({ type: 'pkcs8', format: 'pem' })
const d = journeyDates()

// ---- the simulated bank: synthetic data, shaped as the Enable Banking documentation describes ----

const eur = (amount) => ({ currency: 'EUR', amount })
const debit = (ref, day, amount, creditor, text) => ({
  entry_reference: ref, transaction_amount: eur(amount), credit_debit_indicator: 'DBIT', status: 'BOOK', booking_date: d.day(day), value_date: d.day(day),
  creditor: { name: creditor }, remittance_information: text,
})
const transactions = [
  // Exactly the rent: Ledger confirms it on its own.
  debit('journey-rent', 2, '800.00', 'Example Landlord', ['RENT ' + d.prevMonth]),
  // Two identical payments for one hosting invoice: ambiguous, so only suggestions.
  debit('journey-host-1', 12, '20.00', 'Hosting Provider Ltd', ['HOSTING INVOICE ' + d.prevMonth]),
  debit('journey-host-2', 12, '20.00', 'Hosting Provider Ltd', ['HOSTING INVOICE ' + d.prevMonth]),
  // Unrelated: matches no commitment.
  debit('journey-shop', 9, '23.17', 'Example Shop Ltd', ['CARD PURCHASE']),
]
const control = { outage: false, revoked: false }
const state = { sessions: new Map(), n: 0, redirectByState: new Map(), refreshes: 0 }

const json = (res, status, body) => {
  res.writeHead(status, { 'Content-Type': 'application/json' })
  res.end(JSON.stringify(body))
}
const fail = (res, status, error) => json(res, status, { message: error, code: status, error })

const bank = createServer((req, res) => {
  const url = new URL(req.url, 'http://bank.invalid')
  const path = url.pathname
  // The page the user is sent to for consent: approving sends the browser back to Ledger with a code.
  if (req.method === 'GET' && path === '/authorize') {
    const back = state.redirectByState.get(url.searchParams.get('state') ?? '')
    if (!back) return fail(res, 404, 'UNKNOWN_STATE')
    res.writeHead(302, { Location: back + '?state=' + encodeURIComponent(url.searchParams.get('state')) + '&code=synthetic-authorization-code' })
    return res.end()
  }
  if (req.method === 'POST' && path.startsWith('/__control/')) {
    const name = path.slice('/__control/'.length)
    if (!(name in control)) return fail(res, 404, 'UNKNOWN_CONTROL')
    control[name] = url.searchParams.get('on') !== 'false'
    return json(res, 200, control)
  }
  if (!(req.headers.authorization ?? '').startsWith('Bearer ')) return fail(res, 401, 'UNAUTHORIZED_ACCESS')
  let raw = ''
  req.on('data', (c) => (raw += c))
  req.on('end', () => {
    const body = raw ? JSON.parse(raw) : {}
    const account = (uid) => ({
      account_id: { iban: 'XX00JOURNEY0000000001' }, name: 'Journey Holder', details: 'Everyday account', product: 'Current account', currency: 'EUR',
      cash_account_type: 'CACC', uid, identification_hash: 'journey-hash-eur', identification_hashes: ['journey-hash-eur'],
    })
    if (req.method === 'GET' && path === '/application') {
      return json(res, 200, { name: 'Journey application', kid: 'journey', environment: 'SANDBOX', redirect_urls: [], active: true, countries: ['FI'], services: ['AIS'] })
    }
    if (req.method === 'GET' && path === '/aspsps') {
      return json(res, 200, { aspsps: [{ name: 'Example Bank', country: 'FI', psu_types: ['personal'], auth_methods: [{ name: 'redirect', approach: 'REDIRECT', hidden_method: false, psu_type: 'personal' }], maximum_consent_validity: 15552000, beta: false }] })
    }
    if (req.method === 'POST' && path === '/auth') {
      const id = 'journey-auth-' + ++state.n
      state.redirectByState.set(body.state, body.redirect_url)
      return json(res, 200, { url: 'http://127.0.0.1:' + BANK_PORT + '/authorize?state=' + encodeURIComponent(body.state), authorization_id: id, psu_id_hash: 'journey-psu' })
    }
    if (req.method === 'POST' && path === '/sessions') {
      if (body.code !== 'synthetic-authorization-code') return fail(res, 422, 'WRONG_CODE')
      const id = '00000000-0000-4000-9000-' + String(++state.n).padStart(12, '0')
      const uid = 'journey-uid-' + state.n
      state.sessions.set(id, { uid })
      const validUntil = new Date(Date.now() + 90 * 864e5).toISOString()
      return json(res, 200, { session_id: id, accounts: [account(uid)], aspsp: { name: 'Example Bank', country: 'FI' }, psu_type: 'personal', access: { valid_until: validUntil } })
    }
    const sm = /^\/sessions\/([^/]+)$/.exec(path)
    if (sm) {
      const s = state.sessions.get(sm[1])
      if (!s) return fail(res, 404, 'SESSION_DOES_NOT_EXIST')
      if (req.method === 'DELETE') return json(res, 200, { message: 'OK' })
      const validUntil = new Date(Date.now() + 90 * 864e5).toISOString()
      return json(res, 200, { status: control.revoked ? 'REVOKED' : 'AUTHORIZED', accounts: [s.uid], accounts_data: [{ uid: s.uid, identification_hash: 'journey-hash-eur', identification_hashes: ['journey-hash-eur'] }], aspsp: { name: 'Example Bank', country: 'FI' }, psu_type: 'personal', psu_id_hash: 'journey-psu', created: new Date().toISOString(), access: { valid_until: validUntil } })
    }
    const am = /^\/accounts\/([^/]+)\/(details|balances|transactions)$/.exec(path)
    if (req.method === 'GET' && am) {
      if (control.revoked) return fail(res, 401, 'REVOKED_SESSION')
      if (control.outage) return fail(res, 503, 'ASPSP_ERROR')
      if (am[2] === 'details') return json(res, 200, account(am[1]))
      if (am[2] === 'balances') {
        // The balance is as of the first day of the previous month: payments recorded since are not in it.
        return json(res, 200, { balances: [{ name: 'Closing booked', balance_amount: eur('1250.40'), balance_type: 'CLBD', reference_date: d.day(1) }] })
      }
      // One page, then done: the first request has no continuation key.
      return json(res, 200, { transactions: url.searchParams.get('continuation_key') ? [] : transactions, continuation_key: null })
    }
    return fail(res, 404, 'WRONG_REQUEST_PARAMETERS')
  })
})
await new Promise((resolve) => bank.listen(BANK_PORT, '127.0.0.1', resolve))

// ---- the real binary: a throwaway database and a user, then serve ----

const env = {
  ...process.env,
  LEDGER_HTTP_LISTEN: '127.0.0.1:' + LEDGER_PORT,
  LEDGER_HTTP_MANAGEMENT_LISTEN: '127.0.0.1:' + (LEDGER_PORT + 10),
  LEDGER_KEK_VALUE: 'journey-only-not-a-secret-0123456789abcdef',
  LEDGER_DATABASE_PATH: join(dir, 'ledger.db'),
  LEDGER_BANKING_REDIRECT_URL: 'http://127.0.0.1:' + LEDGER_PORT + '/connections/callback',
  LEDGER_BANKING_REFRESH_ENABLED: 'false',
  LEDGER_BANKING_ENABLE_BANKING_BASE_URL: 'http://127.0.0.1:' + BANK_PORT,
  LEDGER_BANKING_ENABLE_BANKING_APP_ENABLED: 'true',
  LEDGER_BANKING_ENABLE_BANKING_APP_APP_ID: '0f6c3c1e-1b2a-4c3d-9e8f-123456789abc',
  LEDGER_BANKING_ENABLE_BANKING_APP_ENVIRONMENT: 'sandbox',
  LEDGER_BANKING_ENABLE_BANKING_APP_PRIVATE_KEY: keyPem,
  LEDGER_LOG_LEVEL: 'warn',
  LEDGER_RATES_ENABLED: 'false',
}
const made = spawnSync(bin, ['auth', 'set-password', '--create', 'administrator', JOURNEY_USER], { env, input: JOURNEY_PASSWORD + '\n', encoding: 'utf8' })
if (made.status !== 0) {
  console.error('could not create the journey user:', made.stderr)
  process.exit(1)
}
const server = spawn(bin, ['serve'], { env, stdio: 'inherit' })
const stop = () => {
  server.kill('SIGTERM')
  bank.close()
}
process.on('SIGTERM', stop)
process.on('SIGINT', stop)
server.on('exit', (code) => process.exit(code ?? 0))
