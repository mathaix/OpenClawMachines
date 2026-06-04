---
title: "Create Your Machine"
slug: "create-machine"
order: 2
excerpt: "Sign in, navigate the dashboard, and create your first OpenClaw Machine."
---

# Create Your Machine

This guide walks you through creating your first OpenClaw Machine, from signing in to having a ready-to-configure machine on your dashboard.

## Sign In

Navigate to [openclawmachines.com](https://openclawmachines.com) and click **Reserve a Spot** or go directly to the dashboard. You'll authenticate through Cloudflare Access — sign in with your email or SSO provider.

## The Dashboard

Once signed in, you'll land on the **Dashboard**. This is your home base:

- **Your machines** are listed as cards showing status, name, and quick actions
- **Create new machines** with the "New Machine" button
- **Settings** for your account and API keys are in the sidebar

## Creating a Machine

Click **New Machine** to open the creation form:

### Name

Give your machine a descriptive name. This helps you identify it on the dashboard — something like "Research Agent" or "Code Review Bot".

### Size

Choose the compute resources for your machine:

| Size | vCPUs | RAM | Best For |
|------|-------|-----|----------|
| Small | 2 | 2 GB | Light browsing, simple tasks |
| Medium | 4 | 4 GB | General purpose, most use cases |
| Large | 8 | 8 GB | Heavy computation, multiple browser tabs |

Start with **Medium** if you're unsure — you can always create a new machine with different resources later.

### Configuration

The default configuration works well for most use cases. Advanced users can customize:

- **Agent personality** — system prompt that defines how your agent behaves
- **Channels** — communication channels the agent monitors
- **Skills** — pre-built capabilities you can enable

## After Creation

Your machine will appear on the dashboard in a **stopped** state. Before launching it, you'll need to configure your agent with API keys and any other credentials.

Next: [Configure Your Agent](/docs/configure-agent)
