Stop OpenClaw Machines development services.

Press `Ctrl+C` in the terminal running each service, or:

```bash
# Kill backend / control plane (port 8080)
lsof -ti:8080 | xargs kill -9 2>/dev/null || echo "Backend not running"

# Kill frontend (port 5173)
lsof -ti:5173 | xargs kill -9 2>/dev/null || echo "Frontend not running"
```

To also stop the Docker Postgres started by `scripts/local-dev.sh`:

```bash
bash scripts/local-dev.sh stop
```

If you brought up a local Firecracker worker, tear it down with
`scripts/local-e2e-firecracker.sh down`.
