import { defineConfig, devices } from '@playwright/test'

// The flows run in order against one tenure started by e2e/run.sh, which sets
// E2E_BASE_URL; each flow builds on what the previous ones produced.
// PLAYWRIGHT_CHROMIUM_PATH points at a preinstalled Chromium when the
// Playwright-managed one is not installed.
export default defineConfig({
  testDir: 'flows',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 120_000,
  expect: { timeout: 20_000 },
  reporter: process.env.CI ? [['github'], ['list']] : 'list',
  outputDir: 'test-results',
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8080',
    locale: 'fr-FR',
    timezoneId: 'Europe/Paris',
    trace: 'retain-on-failure',
    serviceWorkers: 'block',
    testIdAttribute: 'data-test',
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        launchOptions: process.env.PLAYWRIGHT_CHROMIUM_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_PATH } : {},
      },
    },
  ],
})
