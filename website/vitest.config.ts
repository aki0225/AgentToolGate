import { configDefaults, defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    exclude: [...configDefaults.exclude, "e2e/**"],
    coverage: {
      provider: "v8",
      reporter: ["text", "json-summary"],
      include: ["src/evaluation/realCodexProof.ts"],
      thresholds: {
        branches: 85,
        functions: 85,
        lines: 85,
        statements: 85
      }
    }
  }
});
