# Server browser runtime

This directory pins the runtime used by the standalone server image:

- Node.js 22.23.4
- `@playwright/mcp` 0.0.84
- Playwright 1.65.0-alpha-2026-10-30 / Chromium 157.0.8103.15

Keep `package.json` and `package-lock.json` in sync with
`hexclaw-desktop/scripts/browser-runtime/`. The Docker `browser-runtime` stage
installs these exact dependencies and generates the manifest consumed by
`internal/browserruntime`.
