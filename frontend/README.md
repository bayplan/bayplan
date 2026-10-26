# Bayplan — frontend

React 19 + TypeScript + Vite. This is the SPA of the Bayplan panel (see the
[root README](../README.md) for what the project is as a whole).

In production it is **not served on its own**: `make build` (from the root) compiles this
package and `internal/web` embeds it (`embed.FS`) inside the Go binary — a single artifact
to deploy, with no separate static file server.

## Commands

```bash
npm run dev       # development server with hot reload (needs the backend running
                   # separately; see vite.config.ts for the /api and /ws proxy)
npm run build     # tsc -b && vite build -> dist/
npm run lint      # oxlint + locale key check (scripts/check-locales.mjs)
npx tsc -b        # type check only (not --noEmit: tsconfig.json is a solution file)
npm run preview   # serves the production build locally
```

## Structure

```
src/
├── api/          # HTTP calls (axios) and WebSocket openers, one file per domain
├── components/   # shared components (Layout, LogsPanel, TerminalPanel, CodeEditor…)
│   ├── nav/      # sidebar, cluster/namespace pickers, command palette (Ctrl/Cmd+K)
│   └── ui/       # design primitives: Button, Card, DataTable, Modal, Menu, Badge, Tabs…
├── hooks/        # useAuth (session), usePreferences, useUrlState, useTableState, useCopy
├── i18n/         # react-i18next; locales/en.json is the source of truth, plus es, ca,
│                 # de, fr, pt-BR, zh-CN and ja (same keys, checked by npm run lint)
├── lib/          # axios client, status tones, namespaces, errors, theme, brand
└── pages/        # one page per route, lazy-loaded from App.tsx
```

- Server state goes through **TanStack Query** (`useQuery`/`useMutation`), never Redux or
  Context for remote data.
- Styling with **Tailwind v4** (CSS-first, tokens in `@theme` in `src/index.css`, used as
  utilities such as `bg-surface` or `text-fg-muted`; theme via the `.dark` class on `<html>`)
  — no separate `tailwind.config.*` file.
- The components in `components/ui/` are the primitives to reuse (tables, cards, status
  badges, buttons); avoid reimplementing their look inline in a new page.
- See "Frontend" and "i18n" in [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) for the
  conventions that should not be reverted.
