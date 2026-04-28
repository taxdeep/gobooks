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
        dashboard: resolve(__dirname, "internal/web/react/dashboard/main.tsx"),
        sales_transactions: resolve(__dirname, "internal/web/react/sales_transactions/main.tsx"),
        bank_reconcile: resolve(__dirname, "internal/web/react/bank_reconcile/main.tsx"),
        pdf_template_editor: resolve(__dirname, "internal/web/react/pdf_template_editor/main.tsx")
      },
      output: {
        entryFileNames: "[name].js",
        chunkFileNames: "[name]-[hash].js",
        assetFileNames: "[name][extname]"
      }
    }
  }
});
