---
title: "Browser Automation"
slug: "browser-automation"
order: 5
excerpt: "How browser automation works in OpenClaw Machines — CDP, Playwright, and the browser companion."
---

# Browser Automation

Every OpenClaw Machine includes a full Chromium browser that your agent can control programmatically. This is what makes OCM agents capable of real web tasks — filling forms, navigating sites, extracting data, and interacting with web applications.

## How It Works

The browser automation stack has three layers:

1. **Chromium** — a full browser running inside the microVM
2. **Chrome DevTools Protocol (CDP)** — the low-level protocol for controlling the browser
3. **Browser Companion** — OCM's agent-friendly interface that translates high-level actions into CDP commands

When your agent decides to interact with a website, it communicates through the browser companion, which handles the complexity of element selection, waiting for page loads, handling popups, and more.

## What Your Agent Can Do

### Navigation
- Open URLs and navigate between pages
- Handle redirects, popups, and new tabs
- Wait for page loads and dynamic content

### Interaction
- Click buttons and links
- Fill out forms and text fields
- Select dropdown options
- Upload files
- Handle authentication flows

### Data Extraction
- Read page content and text
- Extract structured data from tables
- Take screenshots for visual analysis
- Monitor network requests

### Advanced
- Execute JavaScript in the page context
- Intercept and modify network requests
- Handle multi-tab workflows
- Interact with SPAs and dynamic web apps

## Using Playwright

For advanced automation, you can use Playwright directly inside your machine. Playwright is pre-installed and connects to the running Chromium instance via CDP.

```python
from playwright.sync_api import sync_playwright

with sync_playwright() as p:
    # Connect to the existing browser
    browser = p.chromium.connect_over_cdp("http://localhost:9222")
    page = browser.contexts[0].pages[0]

    # Automate
    page.goto("https://example.com")
    page.fill("#search", "OpenClaw Machines")
    page.click("button[type=submit]")
```

This is useful for:
- Complex multi-step workflows
- Custom scraping logic
- Testing and validation
- Integrating with existing Playwright scripts

## Tips

- **Let the agent drive** — in most cases, the browser companion handles everything. Use Playwright only when you need precise control.
- **Screenshots help** — the agent uses screenshots to understand page layout. If it's struggling with a complex page, visual context usually helps.
- **Handle auth carefully** — for sites requiring login, configure credentials in your agent's system prompt or use the secrets system to inject them securely.

Next: [CLI Reference](/docs/cli-reference)
