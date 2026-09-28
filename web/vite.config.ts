import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5273,
    proxy: {
      "/api": {
        target: process.env.DEV_API_URL || "http://127.0.0.1:8181",
        ws: true,
      },
    },
  },
});
