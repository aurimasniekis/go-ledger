import { test as base, type Browser, type BrowserContextOptions } from '@playwright/test'

// Hosts that need Chromium's --single-process (PLAYWRIGHT_SINGLE_PROCESS=1, see
// playwright.config.ts) have a browser that dies once a context closes
// ("Target page, context or browser has been closed" in a later test). There, the worker's browser
// is replaced by a fresh one for every context, so none lives long enough to hit
// that limit (the browser exits when its first context
// closes, so a worker's second test would find it dead). Everywhere else the shared
// per-worker browser is used unchanged.
const singleProcess = process.env.PLAYWRIGHT_SINGLE_PROCESS === '1'

export const test = singleProcess
  ? base.extend<object, { browser: Browser }>({
      browser: [
        async ({ playwright, browserName }, use) => {
          const launch = () => playwright[browserName].launch({ args: ['--single-process'] })
          let current = await launch()
          let used = 0
          // Every property reads through to the current real browser, so Playwright's
          // own bookkeeping sees a genuine Browser; only newContext decides when to relaunch.
          const proxy = new Proxy(current, {
            get(_target, prop) {
              if (prop === 'newContext') {
                return async (options?: BrowserContextOptions) => {
                  if (used >= 1 || !current.isConnected()) {
                    await current.close().catch(() => {})
                    current = await launch()
                    used = 0
                  }
                  used++
                  return current.newContext(options)
                }
              }
              const value = Reflect.get(current, prop, current)
              return typeof value === 'function' ? value.bind(current) : value
            },
          })
          await use(proxy)
          await current.close().catch(() => {})
        },
        { scope: 'worker' },
      ],
    })
  : base

export { expect } from '@playwright/test'
