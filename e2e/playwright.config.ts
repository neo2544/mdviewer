import { defineConfig, devices } from '@playwright/test';

// Fixed local port for the test server. Loopback-only, matching how the
// app is normally run (and what the security middleware allows).
const PORT = 8799;
const BASE_URL = `http://127.0.0.1:${PORT}`;

export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL: BASE_URL,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
  // Build the Go binary and serve the repo root (..) on the test port.
  // cwd defaults to this config's directory (e2e/).
  webServer: {
    command: `cd .. && go build -o e2e/mdviewer_e2e_bin . && cd e2e && ./mdviewer_e2e_bin --web --port ${PORT} --root ..`,
    url: BASE_URL,
    reuseExistingServer: false,
    timeout: 120_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
});
