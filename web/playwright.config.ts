import { defineConfig } from "@playwright/test";
const port = process.env.PLAYWRIGHT_PORT || "5274";
const baseURL = `http://127.0.0.1:${port}`;
const previewPort = Number(port) + 100;
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
  }, {
    // Measure startup from the shipped bundle. Source-module harnesses keep
    // using the dev server above; on-demand Vite compilation is not page load.
    command: `npm run build && npx vite preview --host 127.0.0.1 --port ${previewPort} --strictPort`,
    url: `http://127.0.0.1:${previewPort}`,
    reuseExistingServer: false,
  }],
  reporter: "list",
});
