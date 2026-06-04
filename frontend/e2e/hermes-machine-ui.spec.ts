import { expect, test } from "@playwright/test";

const account = { id: 1, name: "Test Account", slug: "test-account", role: "owner" };

const hermesMachine = {
  id: "11111111-1111-4111-8111-111111111111",
  account_id: 1,
  kind: "hermes",
  name: "Hermes e2e",
  slug: "hermes-e2e",
  status: "running",
  vcpus: 2,
  memory_mb: 4096,
  vm_ip: "10.0.0.2",
  desired_channel: "stable",
  runtime_source: "artifact",
  created_at: new Date().toISOString(),
};

async function mockAuthenticatedApi(page: import("@playwright/test").Page) {
  await page.route("**/api/auth/me", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ user: { id: 1, email: "tester@example.com", name: "Tester" } }),
    }),
  );
  await page.route("**/api/accounts", (route) =>
    route.fulfill({ contentType: "application/json", body: JSON.stringify([account]) }),
  );
  await page.route("**/api/invitations/pending", (route) =>
    route.fulfill({ contentType: "application/json", body: JSON.stringify([]) }),
  );
  await page.route("**/api/sizes", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify([
        {
          id: "standard",
          label: "Standard",
          vcpus: 2,
          memory_mb: 4096,
          swap_mb: 1024,
          disk_gb: 20,
          price_cents_mo: 2900,
          description: "2 vCPU",
          is_default: true,
        },
      ]),
    }),
  );
  await page.route("**/api/regions", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify([{ code: "us-west1", name: "US West", available: true }]),
    }),
  );
  await page.route("**/api/accounts/1/openclaw/releases**", (route) =>
    route.fulfill({ contentType: "application/json", body: JSON.stringify([]) }),
  );
  await page.route("**/api/accounts/1/rootfs/releases**", (route) =>
    route.fulfill({ contentType: "application/json", body: JSON.stringify([]) }),
  );
  await page.route("**/api/accounts/1/machines/11111111-1111-4111-8111-111111111111", (route) =>
    route.fulfill({ contentType: "application/json", body: JSON.stringify(hermesMachine) }),
  );
  await page.route("**/api/accounts/1/machines/11111111-1111-4111-8111-111111111111/usage**", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        machine_id: hermesMachine.id,
        current_month_spend: 0,
        budget_microcents: 0,
        since: new Date().toISOString(),
        records: [],
      }),
    }),
  );
  await page.route("**/api/accounts/1/machines/11111111-1111-4111-8111-111111111111/capabilities", (route) =>
    route.fulfill({ contentType: "application/json", body: JSON.stringify([]) }),
  );
  await page.route("**/api/accounts/1/machines/11111111-1111-4111-8111-111111111111/dashboard/chat**", (route) =>
    route.fulfill({
      contentType: "text/html",
      body: `<!doctype html>
        <title>Hermes Chat</title>
        <script>
          const params = new URLSearchParams(location.search);
          parent.postMessage({
            type: "ocm.hermes.session",
            sessionId: params.get("resume") || "session-from-frame",
            lastActive: params.has("resume") ? Date.now() : 1
          }, location.origin);
        </script>`,
    }),
  );
  await page.route("**/api/accounts/1/machines/11111111-1111-4111-8111-111111111111/dashboard/", (route) =>
    route.fulfill({ contentType: "text/html", body: "<!doctype html><title>Hermes Dashboard</title>" }),
  );
}

test.describe("Hermes machine UI", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test("creates a Hermes machine and opens native webchat", async ({ page }) => {
    let createPayload: Record<string, unknown> | undefined;
    let machineList: typeof hermesMachine[] = [];
    await mockAuthenticatedApi(page);
    await page.route("**/api/accounts/1/machines", async (route) => {
      if (route.request().method() === "POST") {
        createPayload = route.request().postDataJSON();
        machineList = [hermesMachine];
        await route.fulfill({ contentType: "application/json", body: JSON.stringify(hermesMachine) });
        return;
      }
      await route.fulfill({ contentType: "application/json", body: JSON.stringify(machineList) });
    });

    await page.goto("/dashboard");
    await page.getByRole("button", { name: "New Machine" }).click();
    await page.getByRole("button", { name: "Hermes" }).click();
    await page.locator('input[placeholder="my-agent"]').fill("Hermes e2e");
    await page.getByRole("button", { name: "Create" }).click();

    await expect(page).toHaveURL(/\/machines\/11111111-1111-4111-8111-111111111111$/);
    expect(createPayload).toMatchObject({
      name: "Hermes e2e",
      kind: "hermes",
      size: "standard",
      preferred_region: "us-west1",
      auto_start: true,
      runtime_source: "artifact",
    });
    expect(createPayload).not.toHaveProperty("openclaw_version");
    expect(createPayload).not.toHaveProperty("rootfs_version");

    await expect(page.getByRole("heading", { name: "Hermes e2e" })).toBeVisible();
    await expect(page.getByText("Hermes", { exact: true })).toBeVisible();
    await expect(page.getByRole("link", { name: "Webchat" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Terminal" })).toBeVisible();

    await page.evaluate(() => {
      localStorage.setItem(
        "ocm_hermes_chat_session_1:11111111-1111-4111-8111-111111111111",
        "session-123",
      );
    });
    await page.getByRole("link", { name: "Webchat" }).click();
    await expect(page).toHaveURL(/\/chat\/11111111-1111-4111-8111-111111111111$/);
    const frame = page.locator('iframe[title="Chat – Hermes e2e"]');
    await expect(frame).toHaveAttribute(
      "src",
      /\/api\/accounts\/1\/machines\/11111111-1111-4111-8111-111111111111\/dashboard\/chat\?resume=session-123$/,
    );

    await page.getByRole("button", { name: "New chat" }).click();
    await expect(frame).toHaveAttribute(
      "src",
      /\/api\/accounts\/1\/machines\/11111111-1111-4111-8111-111111111111\/dashboard\/chat$/,
    );
    await expect
      .poll(() =>
        page.evaluate(() =>
          localStorage.getItem("ocm_hermes_chat_session_1:11111111-1111-4111-8111-111111111111"),
        ),
      )
      .toBeNull();
  });
});
