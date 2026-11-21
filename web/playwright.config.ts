import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { defineConfig, devices } from '@playwright/test'

// E2E scaffold (F7). It drives a real `ledger serve` with the embedded UI, so build
// the frontend and the binary first (`make build-full`). Tests are offline.
// Run: E2E_LEDGER_BIN=../bin/ledger pnpm test:e2e
const port = 18080
// Opt-in for sandboxed hosts where Chromium crashes: PLAYWRIGHT_SINGLE_PROCESS=1
// starts it with --single-process. Off by default; see CONTRIBUTING.md.
const singleProcess = process.env.PLAYWRIGHT_SINGLE_PROCESS === '1'
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  // Single-process Chromium dies when its first browser context closes, so a worker can run one test only. With
  // the opt-in flag every test gets its own worker (the count is an upper bound, not a number of processes started).
  ...(singleProcess ? { workers: 64 } : {}),
  forbidOnly: !!process.env.CI,
  reporter: 'list',
  use: { baseURL: `http://127.0.0.1:${port}`, trace: 'retain-on-failure' },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        ...(singleProcess ? { launchOptions: { args: ['--single-process'] } } : {}),
      },
    },
  ],
  webServer: {
    command: `${process.env.E2E_LEDGER_BIN ?? '../bin/ledger'} serve`,
    // Synthetic values for a throwaway server: no real key, no real data. The store
    // needs a file path (not :memory:), so each run gets its own temporary database.
    env: {
      LEDGER_HTTP_LISTEN: `127.0.0.1:${port}`,
      LEDGER_KEK_VALUE: 'e2e-only-not-a-secret-0123456789abcdef',
      LEDGER_DATABASE_PATH: join(mkdtempSync(join(tmpdir(), 'ledger-e2e-')), 'ledger.db'),
    },
    url: `http://127.0.0.1:${port}/`,
    reuseExistingServer: false,
    timeout: 30_000,
  },
})
