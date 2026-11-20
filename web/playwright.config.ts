import { defineConfig, devices } from '@playwright/test'

// E2E scaffold (F7). It drives a real `ledger serve` with the embedded UI, so build
// the frontend and the binary first (`make build-full`). Tests are offline.
// Run: LEDGER_E2E_BIN=../bin/ledger pnpm test:e2e
const port = 18080
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  reporter: 'list',
  use: { baseURL: `http://127.0.0.1:${port}`, trace: 'retain-on-failure' },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: `${process.env.LEDGER_E2E_BIN ?? '../bin/ledger'} serve`,
    // Synthetic values for a throwaway server: no real key, no real data.
    env: {
      LEDGER_HTTP_LISTEN: `127.0.0.1:${port}`,
      LEDGER_KEK_VALUE: 'e2e-only-not-a-secret',
      LEDGER_DATABASE_PATH: ':memory:',
    },
    url: `http://127.0.0.1:${port}/`,
    reuseExistingServer: false,
    timeout: 30_000,
  },
})
