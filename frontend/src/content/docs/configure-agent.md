---
title: "Configure Your Agent"
slug: "configure-agent"
order: 3
excerpt: "Set up API keys, agent personality, channels, and skills for your OpenClaw Machine."
---

# Configure Your Agent

Before launching your machine, you need to configure the AI agent that will control it. This involves setting up API credentials, defining the agent's behavior, and optionally enabling skills and channels.

## API Keys & Secrets

Your agent needs at least one AI model API key to function. Go to **Settings** in your dashboard to add credentials.

### Required: AI Model Provider

Add an API key for your preferred model provider:

- **Anthropic** — for Claude models (recommended)
- **OpenAI** — for GPT-4 and other OpenAI models
- **Google** — for Gemini models
- **Other providers** — any OpenAI-compatible API endpoint

The key is securely stored and injected into your machine at boot time. It never touches disk inside the VM.

### Optional: Service Credentials

If your agent needs to interact with external services, add those credentials too:

- **GitHub tokens** — for code repositories
- **Slack tokens** — for team communication
- **Database credentials** — for data access
- **Any API key** — for services your agent will use

## Agent Personality

The agent's **system prompt** defines its behavior, expertise, and communication style. You can configure this per-machine:

- **Role** — "You are a research assistant" or "You are a senior developer"
- **Instructions** — specific guidelines for how the agent should approach tasks
- **Constraints** — things the agent should avoid or be careful about

A good system prompt is specific and actionable. Instead of "be helpful", try "Research topics thoroughly using multiple sources, cite your findings, and present results in a structured format."

## Channels

Channels define how you communicate with your agent:

- **Terminal** — direct command-line interaction (always available)
- **Chat** — conversational interface in the workspace
- **Slack** — receive and respond to Slack messages (requires Slack credentials)

## Skills

Skills are pre-built capabilities you can enable:

- **Web Research** — structured web browsing and information gathering
- **Code Generation** — writing and editing code files
- **Data Analysis** — processing and analyzing data sets
- **Custom Skills** — define your own skill workflows

## Saving Configuration

Configuration is saved to your machine and applied at next boot. You can update configuration at any time — changes take effect on the next machine start.

Next: [Launch & Connect](/docs/launch-and-connect)
