import { defineConfig } from "@playwright/test";
const port = process.env.PLAYWRIGHT_PORT || "5274";
const baseURL = `http://127.0.0.1:${port}`;
export default defineConfig({
  testDir: "./tests",
  fullyParallel: true,
  // Each Chromium page holds a full React bundle and several full-page
  // screenshots; this machine's memory ceiling is lower than the default of 8 workers.
  workers: 2,
  use: { baseURL, headless: true },
  webServer: [{
    command: `npm run dev -- --port ${port} --strictPort`,
    url: baseURL,
    reuseExistingServer: false,
  }, {
    command: "python3 -m http.server 4731 --bind 127.0.0.1 --directory ../design/stars",
    url: "http://localhost:4731",
    reuseExistingServer: true,
  }],
  reporter: "list",
});
