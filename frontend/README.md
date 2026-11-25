# Orchestrator Frontend

## Overview

Orchestrator Frontend is a Web Application that helps with managing Node clusters. It works together with Orchestrator Backend and Orchestrator Agent.

## Tech Stack

- **Environment**: Node (>=22), pnpm (>=9)
- **Basis**: Vite, React, TypeScript
- **Routing**: TanStack Router
- **State Management**: split on purpose — **TanStack Query** for server state,
  **Effector** for client state (`entities/*/model.ts` are Effector stores). Naming only
  TanStack Query here is how client state ends up in the wrong place.
- **Forms**: React Hook Form + Yup
- **Styling**: Tailwind
- **i18n**: i18next

Inspiration for the project structure comes from [Feature Slices](https://feature-sliced.design/docs) approach with a slightly different layer setup:

```
📂 routes
📂 pages
📂 widgets
📂 entities
📂 shared
  📂 assets
  📂 icons
  📂 ui
```

## Development Environment

This project uses VSCode's Devcontainer feature. It's not required to work on the project, but is highly recommended to provide a fast and consistent development environment for everybody.

Install Docker [on your development machine](https://code.visualstudio.com/docs/devcontainers/containers#installation), or

Use Devcontainers [on a remote development machine](https://code.visualstudio.com/remote/advancedcontainers/develop-remote-host)

## Development Dependencies

Orchestrator Frontend needs `Node`, `npm`, and `pnpm` in order to function. If you're using the included **Devcontainer**, these are installed and managed automatically.

Otherwise, check the corresponding documentation for installation instructions for each dependency for your current development platform.

## Development Project Setup

1. Clone the repository

   The frontend is a **directory of the `uptime-lab/orchestrator` monorepo**, not a
   standalone repository.

   ```sh
   git clone git@github.com:uptime-lab/orchestrator.git
   cd orchestrator/frontend
   ```

2. Install application dependencies using pnpm

   ```sh
   pnpm i
   ```

3. Run Vite development webserver

   ```sh
   pnpm run dev # the app should start on 5174 port
   ```

## UI Sample Page

The project includes an all-in-one page where designers and developers can see styled UI components for easy inspection. Visit the URL `http://<webserver>:<port>/ui-samples`.

**Development builds only** — the route redirects to `/` unless `import.meta.env.DEV`, so it
is deliberately absent from production builds.

## Environment Variables

The application supports the following environment variables that can be configured at runtime:

- `API_URL` - The URL of the API server
- `WEBSOCKET_URL` - The URL of the WebSocket server
- `ESSENTIALS_API_ONLY` - When set to 'on', enables essential API mode which limits functionality
- `TERMS_URL` - An `https://` link to the operator's own terms of use. When set, the sign-up
  form shows a required "I agree to the Terms of Use" checkbox linking to it, and the account
  menu shows a "Terms of Use" link. Unset, empty or not `https://` = nothing is shown and
  sign-up asks for no agreement. The product ships no terms text of its own. The acceptance is
  sent with the sign-up request (`terms_accepted`) and stored by the backend with a timestamp
  and an audit event.

These variables can be set in two ways:

1. During development:
   Dev builds IGNORE `VITE_API_URL` — `getApiUrl()` always returns `/api` in
   development, and the Vite dev server proxies `/api` to the backend named by
   **`VITE_DEV_BACKEND_URL`** (`vite.config.ts`), stripping the prefix. So the
   variable that actually points a dev session at a backend is the proxy
   target, not the API URL. Create a `.env` in the project root:
   ```
   # where the Vite proxy forwards /api: your local backend. `make backend/run`
   # listens on 8080 with config/config.yaml.example copied to
   # config_local/config.yaml, or on whatever port your config_local sets.
   VITE_DEV_BACKEND_URL=http://localhost:8080
   VITE_ESSENTIALS_API_ONLY=off
   VITE_TERMS_URL=
   ```
   `VITE_API_URL` / `VITE_WEBSOCKET_URL` matter only for production-style
   builds, where the container substitutes their unprefixed twins into
   `public/config.js`.

2. During production:
   Pass the environment variables when running the Docker container:
   ```bash
   docker run -e API_URL=http://localhost:5174/api \
             -e WEBSOCKET_URL=ws://localhost:5174/api \
             -e ESSENTIALS_API_ONLY=off \
             -e TERMS_URL=https://example.com/terms \
             your-image-name
   ```
