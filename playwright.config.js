import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "internal/htmlview/browser",
  globalSetup: "./internal/htmlview/browser/render.js",
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? "github" : "list",
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "firefox", use: { ...devices["Desktop Firefox"] } },
  ],
});
