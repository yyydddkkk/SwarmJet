import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./tests",
  fullyParallel: true,
  // Each Chromium page holds a full React bundle and several full-page
  // screenshots; this machine's memory ceiling is lower than the default of 8 workers.
  workers: 2,
  use: { baseURL: "http://127.0.0.1:5274", headless: true },
  webServer: {
    command: "npm run dev -- --port 5274 --strictPort",
    url: "http://127.0.0.1:5274",
    reuseExistingServer: false,
  },
  reporter: "list",
});
