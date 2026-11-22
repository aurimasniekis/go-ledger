import '@testing-library/jest-dom/vitest'
import { cleanup, configure } from '@testing-library/react'
import { afterEach } from 'vitest'

// jsdom does not implement scrolling; the router calls it on navigation.
window.scrollTo = () => {}

// findBy*/waitFor default to 1000 ms. The first render of a routed page boots
// the app (session, lazy route chunks, queries) and under a loaded machine
// that alone can exceed a second, failing tests that are otherwise correct.
// Resolved waits return as soon as the condition holds, so a generous ceiling
// costs nothing on a passing run; it only bounds a genuine failure.
configure({ asyncUtilTimeout: 5000 })

afterEach(() => cleanup())
