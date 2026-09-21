import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./tests",
  fullyParallel: true,
  use: { baseURL: "http://127.0.0.1:5274", headless: true },
  webServer: {
    command: "npm run dev -- --port 5274 --strictPort",
    url: "http://127.0.0.1:5274",
    reuseExistingServer: false,
  },
  reporter: "list",
});
