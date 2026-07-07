# OpenClaw Machines — videos (Remotion)

Two 1080p compositions:

- **OpenClawDemo** — a real product demo: provision a host → spin up an
  instance → open the running VM → connect MCP tools → the agent calls a tool,
  narrated with captions. Built from real screenshots of the running app (in
  `public/shots/`, captured by `capture.mjs`) plus animated host-provisioning
  and a terminal panel showing an actual `ocm.call_tool` request/response.
- **OpenClawMachines** — an abstract animated feature montage.

## Render

```bash
npm install
npm run render:demo
npm run render:machines
npx remotion render src/index.ts OpenClawDemo out/openclaw-demo.mp4 --codec=h264
npx remotion render src/index.ts OpenClawMachines out/openclaw-machines.mp4 --codec=h264
npm start   # Remotion Studio to preview/edit either composition
```

## Re-capture the real screenshots

With the app running locally (backend :8080, frontend :5173, dev auth on):

```bash
node capture.mjs   # writes captures/*.png -> copy into public/shots/
```

`capture.mjs` drives the real UI with Playwright at 1920x1080@2x. Brand palette
and the claw logo path live in `src/theme.ts` and `src/components/Logo.tsx`.
