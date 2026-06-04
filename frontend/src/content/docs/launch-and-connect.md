---
title: "Launch & Connect"
slug: "launch-and-connect"
order: 4
excerpt: "Start your machine, access the workspace, and verify your agent is running."
---

# Launch & Connect

With your machine created and configured, it's time to launch it and connect to the workspace.

## Starting Your Machine

From the dashboard, click the **Start** button on your machine card. The machine boots in seconds:

1. **Provisioning** — a Firecracker microVM is created with your chosen resources
2. **Booting** — the Linux environment starts, networking is configured
3. **Agent startup** — the AI gateway and agent process initialize
4. **Ready** — the status indicator turns green

The entire process typically takes under 10 seconds.

## The Workspace

Click **Open Workspace** to enter your machine's workspace. This is a full-screen environment with several panels:

### Terminal

The terminal gives you direct shell access to the machine. You can:

- Run commands manually
- Watch agent activity in real time
- Interact with the agent via the command line
- Access logs and debug output

### Browser View

See what your agent sees. The browser panel shows the agent's browser session, so you can:

- Watch the agent navigate websites
- See form fills and clicks as they happen
- Verify the agent is on the right track

### Gateway Dashboard

The gateway dashboard shows:

- **Agent status** — is the agent active, idle, or processing?
- **Model usage** — tokens consumed, requests made
- **Activity log** — what the agent has been doing

## Verifying the Agent

After launch, verify everything is working:

1. **Check the terminal** — you should see the agent process running
2. **Open the gateway dashboard** — confirm the AI model connection is active
3. **Give it a task** — try a simple request like "search the web for today's news"

If the agent isn't responding, check:

- API key is correctly configured in Settings
- The machine has enough resources for the chosen model
- Network connectivity is working (check the gateway dashboard)

## Stopping Your Machine

When you're done, click **Stop** on the dashboard. The machine state is preserved — you can restart it later and the agent picks up where it left off.

Next: [Browser Automation](/docs/browser-automation)
