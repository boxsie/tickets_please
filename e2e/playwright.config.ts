import { defineConfig } from '@playwright/test';

// E2E config for the tickets_please web UI. The webServer block spawns a
// dedicated `tickets_please serve` instance on port 18900 with a tempdir
// data root, so the tests don't touch the real /home/dan/.tickets_please
// data or the running systemd service on :8765.
export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  expect: { timeout: 5_000 },
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL: 'http://127.0.0.1:18900',
    headless: true,
    viewport: { width: 1280, height: 900 },
    screenshot: 'on',
    trace: 'on-first-retry',
  },
  webServer: {
    command: 'bash ./scripts/start-test-server.sh',
    url: 'http://127.0.0.1:18900/healthz',
    reuseExistingServer: false,
    timeout: 20_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
  projects: [
    {
      name: 'chromium',
      use: {
        // Use the pre-installed headless shell that ships with Chromium.
        channel: undefined,
      },
    },
  ],
});
