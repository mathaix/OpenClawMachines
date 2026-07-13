import { test, expect } from "@playwright/test";

// Spot E2E smoke (CI: ci/spot-e2e.sh, project chromium-dev, AUTH_MODE=dev).
//
// Validates the provisioning path against a real spot host: create a machine via
// the dashboard modal (which submits auto_start, so the machine provisions
// immediately — there is no separate "Start" step), wait for the Firecracker
// microVM to boot (status "running"), then stop + delete. It intentionally does
// NOT open the workspace / assert the in-VM OpenClaw gateway — those need a
// deployed data-plane Worker and are covered by machine-lifecycle.spec.ts.
test.describe.serial("Spot host provisioning smoke", () => {
  // One machine on one spot host: a Playwright retry would create a second
  // machine that can't be placed (host capacity), so disable retries here.
  // Timeout must be set HERE, not via test.setTimeout() in the body: it covers
  // "Before Hooks" (first browser launch took 31.7s on the loaded 2-core
  // runner in run 27372764292, blowing the default 30s before the body ran).
  test.describe.configure({ retries: 0, timeout: 300_000 });
  const name = `ci-${Date.now()}`;
  let machineUrl = "";

  test("create a machine (placed on the spot host) and it boots", async ({ page }, testInfo) => {
    // Ground truth in the CI log: runs 27372764292/27373609550 reported "Test
    // timeout of 30000ms exceeded" despite the configure() above, which local
    // replication cannot reproduce; ci/spot-e2e.sh now also passes --timeout.
    console.log("spot-smoke effective timeout:", testInfo.timeout);
    await page.goto("/dashboard");

    // Open the create modal (CreateMachineModal) and submit. Defaults are fine:
    // OpenClaw kind, Standard size, the auto-selected region (the ready host), and
    // "Latest (stable)" rootfs/openclaw. The modal sends auto_start:true.
    await page.getByRole("button", { name: "New Machine" }).click();
    const modal = page.getByRole("dialog");
    await modal.getByPlaceholder("my-agent").fill(name);
    await modal.getByRole("button", { name: "Create", exact: true }).click();

    // Lands on the machine detail page (/machines/<id>) and provisions immediately.
    await expect(page).toHaveURL(/\/machines\/[a-f0-9-]+/, { timeout: 15_000 });
    machineUrl = page.url();
    await expect(page.getByRole("heading", { name })).toBeVisible();

    // The VM boots on the spot host. Wait for running via the API, not the
    // live page: two runs (27373609550, 27374764109) froze exactly ~70s into a
    // UI-only wait and never recovered despite the machine reaching running
    // (page.request bypasses the renderer). NOT the "Stop" button either: it
    // also renders during provisioning/starting as an abort escape hatch (run
    // 27372764292 matched it in 36ms with the machine still booting).
    const machineId = machineUrl.split("/").pop()!;
    await expect
      .poll(async () => {
        const resp = await page.request.get(`/api/accounts/1/machines/${machineId}`);
        return resp.ok() ? ((await resp.json()).status as string) : `http-${resp.status()}`;
      }, { timeout: 240_000, intervals: [5_000] })
      .toBe("running");

    // Then assert the running UI on a fresh load: the "Workspace" link renders
    // only in the status === "running" branch of MachineView.
    await page.reload();
    await expect(page.getByRole("link", { name: "Workspace", exact: true })).toBeVisible({ timeout: 30_000 });

    // Labeled validation evidence in the artifact/report.
    await testInfo.attach("firecracker-running", {
      body: await page.screenshot({ fullPage: true }),
      contentType: "image/png",
    });
  });

  test("stop and delete", async ({ page }) => {
    test.setTimeout(120_000);
    // Don't fall through to goto("") → dashboard if the create test died
    // before capturing the URL (it would "pass" against the wrong page).
    test.skip(!machineUrl, "create test did not produce a machine URL");
    await page.goto(machineUrl);

    await page.getByRole("button", { name: "Stop" }).click();
    // "Start" reappears (alongside "Delete") only once the machine is stopped — a
    // unique signal, unlike the "stopped" status text which appears in several
    // places on the page.
    await expect(page.getByRole("button", { name: "Start" })).toBeVisible({ timeout: 60_000 });

    await page.getByRole("button", { name: "Delete" }).click();
    // ConfirmDialog (Radix AlertDialog): type the machine name to enable Delete.
    const dialog = page.getByRole("alertdialog");
    await expect(dialog.getByText("Delete Machine")).toBeVisible();
    await dialog.getByRole("textbox").fill(name);
    await dialog.getByRole("button", { name: "Delete" }).click();

    await expect(page).toHaveURL(/\/dashboard$/, { timeout: 10_000 });
  });
});
