import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const apiTarget = process.env.MMWX_API_TARGET;
const customApiTarget = process.env.MMWXC_API_TARGET ?? "http://127.0.0.1:12890";

const proxy = {
  "/api/custom": {
    target: customApiTarget,
    changeOrigin: true,
    secure: false,
    rewrite: (path: string) => path.replace(/^\/api\/custom/, "/api"),
  },
  ...(apiTarget
    ? {
        "/api": {
          target: apiTarget,
          changeOrigin: true,
          secure: true,
          ws: true,
        },
      }
    : {}),
};

export default defineConfig({
  plugins: [react()],
  server: {
    proxy,
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks: {
          react: ["react", "react-dom"],
          charts: ["recharts"],
        },
      },
    },
  },
});
