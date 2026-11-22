import { spawn, type ChildProcess } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expect, test } from './fixtures'

// A bank's return reaching a signed-out browser (A6g/A6h): the single-use consent code is never kept. The page is
// sent to sign-in with a notice, history never brings the code back, and the server's own log (read here from a
// dedicated instance of the binary, at info level) has the path of the request but not its query. Offline.
// Not 18095: that is the commitments server's management port (18085 + 10, see playwright.config.ts).
const port = 18087
const bin = process.env.E2E_LEDGER_BIN ?? '../bin/ledger'
test.use({ baseURL: `http://127.0.0.1:${port}` })

let server: ChildProcess
let log = ''

test.beforeAll(async () => {
  server = spawn(bin, ['serve'], {
    env: {
      ...process.env,
      LEDGER_HTTP_LISTEN: `127.0.0.1:${port}`,
      LEDGER_HTTP_MANAGEMENT_LISTEN: `127.0.0.1:${port + 10}`,
      LEDGER_BANKING_REFRESH_ENABLED: 'false',
      LEDGER_KEK_VALUE: 'e2e-only-not-a-secret-0123456789abcdef',
      LEDGER_DATABASE_PATH: join(mkdtempSync(join(tmpdir(), 'ledger-e2e-cb-')), 'ledger.db'),
      LEDGER_LOG_LEVEL: 'info',
    },
  })
  server.stdout?.on('data', (b) => (log += String(b)))
  server.stderr?.on('data', (b) => (log += String(b)))
  await expect.poll(async () => (await fetch(`http://127.0.0.1:${port}/`).catch(() => null))?.status, { timeout: 20_000 }).toBe(200)
})
test.afterAll(() => {
  server?.kill()
})

test('a signed-out bank return lands on sign-in with the notice, and neither history nor the server log keeps the code', async ({ page }) => {
  await page.goto('/connections/callback?state=S3CRET-STATE&code=S3CRET-CODE')
  await expect(page).toHaveURL(/\/sign-in/)
  await expect(page.getByTestId('bank-incomplete')).toContainText('not completed')
  const here = decodeURIComponent(page.url())
  expect(here).not.toMatch(/S3CRET|callback/)

  // Going back leaves the app (the callback entry was replaced): the code is not in any entry the user can return to.
  await page.goBack()
  expect(decodeURIComponent(page.url())).not.toContain('S3CRET')
  await page.goForward().catch(() => undefined)
  expect(decodeURIComponent(page.url())).not.toContain('S3CRET')

  // The access log recorded the callback request by path only.
  await expect.poll(() => log).toContain('/connections/callback')
  expect(log).not.toMatch(/S3CRET|state=|code=/)
})
