# Git Ignore List

The root `.gitignore` is the source of truth. This page groups its rules by the
local files they keep out of commits; update this inventory when those rules
change.

## Local data and credentials

- `.env*`, except `.env.example`, plus `.settings-key` and `/new-api-login-key.md`
- `data/`, `.local/`, `*.db`, `*.db-*`, `*.sqlite`, `*.sqlite3`, `*.bak`, `*.tmp`
- Local investigation notes: `/.local_active_refs.txt` and
  `/.local_backend_refs.txt`

## Build and distribution output

- `.next/`, `out/`, `node_modules/`, `coverage/`, `dist/`, `artifacts/`,
  `test-evidence/`, and `build/depth-runtime/*.zip`
- Desktop build output under `backend/cmd/desktop/build/`; generated Wails
  bindings under `backend/cmd/desktop/frontend/wailsjs/`
- Local backend binaries: `backend/cmd/server/server`, `backend/server`,
  `backend/desktop`
- Packaged plugin archives: `plugin-packages/*.beeftv-plugin`
- Frontend `backend/cmd/desktop/frontend/dist/` is ignored except for its
  tracked `.gitkeep` placeholder

## Local tools and editor state

- `open-storyboard-canvas/`, `.cache/`, `.claude/`, `.cursor/`, `.serena/`,
  `.spec-workflow/`, `.superdesign`, `.workbuddy/`, `.worktrees/`
- `.idea/`, `.fleet/`, `.history/`, `.vscode/`, `.DS_Store`
- `.playwright-mcp/`, `.playwright-cli/`, `web/.playwright/`

## Generated probes and review artifacts

- `output/`, `project-memory/`, `backend/tmp_*`, `web/.libtv-*`,
  `web/test/.agent-reliability-*`,
  `web/test/.local-workspace-stale-*`,
  `web/test/.timeline-persist-http-*`
- Browser and UI scratch patterns in `web/`: `.*audit*`, `.round-*`,
  `.smoke-*`, `.probe-*`, `.gated-*`, `.path*`, `.selected*`, `.threshold*`,
  `.count-*`, `.dense-*`, `.directional-*`, `.director-*`, `.final-*`,
  `.oneendpoint-*`, `.panfix-*`, `.reference-*`, `.canvas-probe-*`
- Root and prototype backup drawings: `/*.drawio.bkp`,
  `assets/prototypes/.*.drawio.bkp`
- Generated screenshots: `/prompt-preferences-*-before.png`, `/prompt*-before.png`
- `web/package.json.md5`

## Logs and Python cache

- `*.log`, `*.tsbuildinfo`
- `tools/depth-capture/**/__pycache__/`, `*.py[cod]`

## Local documentation files

- `camera_language_guide.md`
