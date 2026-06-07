Start OpenClaw Machines development services (control plane + frontend).

The control plane needs Postgres + env, so use the local-dev make targets — they
create `.env.local`, start a Docker Postgres, run migrations, then launch the
server. Run each in its own terminal:

**Backend / control plane (port 8080):**
```bash
make local-backend
```

**Frontend (port 5173):**
```bash
make local-frontend
```

The frontend proxies `/api` to the backend automatically.
(`make local-backend` / `make local-frontend` wrap `scripts/local-dev.sh backend|frontend`.)

To also stand up a local Firecracker worker and provision a real microVM (KVM host
required), use `scripts/local-e2e-firecracker.sh up` — see `docs/local-firecracker-e2e.md`.

> Lower-level alternative (assumes Postgres + env are already configured in the shell):
> `make backend` (`go run ./cmd/server/`) and `make frontend` (`npm run dev`).
