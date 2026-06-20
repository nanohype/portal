# portal — web

The React single-page app for portal. Served by the `web` container in
production; run against the local API with Vite in dev.

## Stack

- **Vite 8** + **React 19** + **TypeScript**
- **Tailwind CSS 4** — neutral dark palette with teal primary; tokens in `src/index.css`
- **TanStack Query** for server state, **Zustand** for local UI state
- **TanStack Router** (`src/router.tsx`) — auth-gated layout route, typed params
- **openapi-fetch** client (`src/api/client.ts`) over the hand-maintained
  `src/api/types.ts` contract — there is **no codegen**; edit `types.ts` by hand
  when the API changes
- **xterm.js** for run-log streaming over WebSocket; **sonner** for toasts

## Develop

```bash
npm install
npm run dev          # Vite dev server on :5173 (proxies the API on :8080)
```

From the repo root, `task dev:web` does the same alongside the server + worker.

## Verify

```bash
npx tsc -b           # the real typecheck — the root tsconfig uses project references
npx vite build       # production build
npm audit --audit-level=high
```

## Layout

- `src/components/` — organized by domain (`workspace/`, `pipeline/`, `run/`, `cluster/`, `tenant/`, `teams/`, `settings/`, `ui/`)
- `src/components/ui/` — the design-system primitives (Dialog, Button, Drawer, …)
- `src/routes/` — public routes (Login, AuthCallback)
- `src/stores/` — Zustand stores (auth)
- `src/lib/` — query client, router ref, utilities
