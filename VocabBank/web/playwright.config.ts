import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: 'tests',
  fullyParallel: false,
  workers: 1,
  timeout: 120000,
  reporter: 'list',
  use: {
    actionTimeout: 10000,
    baseURL: 'https://127.0.0.1:8099',
    ignoreHTTPSErrors: true,
    trace: 'retain-on-failure',
  },
  webServer: {
    command:
      'cd .. && VOCAB_BROWSER_FIXTURE=1 GOCACHE="$PWD/.cache/go-build" go test ./internal/httpapp -run TestBrowserFixture -count=1 -timeout=10m -v',
    url: 'https://127.0.0.1:8099',
    ignoreHTTPSErrors: true,
    reuseExistingServer: false,
    timeout: 120000,
  },
});
