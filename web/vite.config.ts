import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/upload": { target: "http://localhost:8080", changeOrigin: true },
      "/images": { target: "http://localhost:8080", changeOrigin: true },
      "/events": { target: "http://localhost:8080", changeOrigin: true },
      "/admin": { target: "http://localhost:8080", changeOrigin: true },
      "/metrics": { target: "http://localhost:8080", changeOrigin: true },
    },
  },
});
