import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { resolve } from "node:path";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "internal/web/static/react",
    emptyOutDir: false,
    sourcemap: false,
    minify: true,
    rollupOptions: {
      input: {
        sales_transactions: resolve(__dirname, "internal/web/react/sales_transactions/main.tsx")
      },
      output: {
        entryFileNames: "[name].js",
        chunkFileNames: "[name]-[hash].js",
        assetFileNames: "[name][extname]"
      }
    }
  }
});
