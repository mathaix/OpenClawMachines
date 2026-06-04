# Design: VM Billing Flow

**Status:** Proposed
**Scope:** Platform billing for Machines/VMs

## Summary

This document defines how OpenClaw Machines should charge for VM hosting.

It does **not** cover LLM API usage billing. LLM costs remain separate and are either:

- bring-your-own-key costs paid directly to Anthropic/OpenAI/Google
- managed model billing in a future phase

VM hosting should use a simple subscription model:

- billing unit: **1 Machine**
- cadence: **monthly**
- billing owner: **account**
- quantity: **number of paid Machines on the account**

The first paid conversion should happen from the existing dashboard and `New Machine` flow. There is no separate account creation paywall.

## Decisions

### 1. Charge per Machine per month

Recommended pricing model:

- each provisioned Machine is a billable unit
- billing is monthly, not hourly
- adding a Machine mid-cycle prorates the added seat
- deleting a Machine reduces the billed quantity on the next cycle for v1
- stopping a Machine does not stop billing

Reasoning:

- users think of a Machine as a persistent agent, not as raw compute minutes
- the product includes storage, secrets, hostname, lifecycle management, and support even when a Machine is stopped
- monthly pricing is easier to understand than runtime metering

### 2. No 7-day free trial subscription for v1

Instead of a time-based trial, the recommended v1 approach is:

- collect payment details up front
- run Checkout immediately before first launch
- automatically apply a **one-time first invoice discount** to the first Starter Machine

This should be an automatic promotion or first-invoice credit, not a user-entered promo code.

Reasoning:

- avoids trial expiry logic
- reduces abuse compared to no-card trials
- keeps access control simpler
- avoids training users to search for coupon fields

### 3. More than one Machine is normal paid quantity billing

The account should not keep one Machine permanently free while charging only for extras.

Instead:

- first Machine: checkout with automatic first-invoice discount
- second Machine and beyond: normal paid quantity billing

Reasoning:

- simpler billing math
- easier invoice explanations
- fewer edge cases when users upgrade, delete, or downgrade Machines

### 4. Admin/tester access uses backend billing overrides

Internal users and testers should bypass billing with an explicit backend override, not a hidden UI shortcut.

## Product Principles

- billing is account-level, even though the user experiences it during Machine creation
- payment happens at the moment the user is ready to launch value, not at sign-up
- the app remains the source of truth for Machine count
- Stripe is the source of truth for payment state
- backend checks enforce billing eligibility; frontend only reflects it

## User-Facing Billing Flow

### Flow A: First Machine

1. User signs in and lands on the dashboard.
2. User clicks `New Machine`.
3. User configures the Machine.
4. User clicks `Launch Machine`.
5. App detects there is no active paid subscription for this account.
6. App sends the user to Checkout.
7. Checkout creates a monthly subscription for quantity `1`.
8. The first invoice includes an automatically applied introductory discount.
9. After Checkout success, the user returns to the app.
10. The Machine launches.

Suggested user copy:

- `Start your first Machine`
- `Billed monthly per Machine`
- `Your first Machine gets an automatic launch discount`

### Flow B: Add a second Machine

1. User clicks `New Machine`.
2. User configures Machine #2.
3. User clicks `Launch Machine`.
4. App detects an active subscription with quantity `1`.
5. App shows a confirmation step:
6. `This will add a second Machine to your monthly subscription.`
7. On confirm, the app updates the subscription quantity to `2`.
8. Stripe prorates the added quantity for the rest of the cycle.
9. The Machine launches.

Suggested user copy:

- `Add this Machine to your plan`
- `Your subscription will update from 1 to 2 Machines`
- `You will be charged a prorated amount for the rest of this billing period`

### Flow C: Delete a Machine

1. User deletes a Machine.
2. The Machine is removed from the account.
3. Billed quantity decreases at the next billing cycle for v1.

Reasoning:

- simpler than issuing immediate credits
- avoids confusion when users rapidly create/delete Machines

Future option:

- immediate proration credits once billing operations are stable

### Flow D: Stop a Machine

Stopping a Machine does not change billing.

Reasoning:

- stopped Machines still reserve persistent product value: disk, configuration, secrets, identity, recovery path

### Flow E: Upgrade a Machine size

If size-based pricing exists:

- Machine size change should update the effective monthly price immediately
- Stripe should prorate the price difference

If size-based pricing does not exist in v1:

- treat all Machines as the same billable unit and leave size out of billing

## Recommended Pricing Shape

For v1, choose one of these two models:

### Option 1: Flat per Machine

- every Machine costs the same monthly amount
- simplest operationally

### Option 2: Tiered per Machine size

- Starter Machine: lower monthly price
- Pro Machine: higher monthly price

Recommendation:

- use flat per Machine pricing if billing simplicity matters most
- use per-size pricing only if infrastructure cost differences are large enough to matter immediately

## Discount Model

Recommended discount behavior:

- automatic, not code-based
- one-time, account-scoped
- only for the first paid Machine
- attached during Checkout by the backend

Avoid as the main UX:

- manual promo code entry
- public shareable discount codes
- repeated intro discounts on the same account

## Billing UI

### Dashboard surfaces

Show billing state in three places:

- launch step
- dashboard header or account summary
- settings billing page

### Billing page should show

- subscription status: `active`, `past_due`, `canceled`, `internal`, `tester`
- billed Machine count
- per-Machine monthly price
- next invoice date
- payment method
- recent invoices
- `Manage billing` action

### Launch interstitials

When billing is required, do not fail silently. Show an explicit step before provisioning:

- first Machine: `Complete billing to launch`
- extra Machine: `Confirm plan update`
- payment problem: `Update payment method to launch another Machine`

## Admin / Tester Bypass

Admin and QA bypass should be account-level and explicit.

### Recommended fields

- `billing_source = stripe | admin_override`
- `billing_override_type = internal | tester | comped`
- `billing_override_reason`
- `billing_override_expires_at`
- `billing_override_granted_by`

### Recommended behavior

- `internal`: full bypass for team-operated accounts
- `tester`: temporary bypass for QA or external testers
- `comped`: free access for selected customer accounts

### Rules

- bypass is enforced on the backend
- bypass accounts should not be sent to Checkout
- UI should visibly show override state
- all override changes should be audit logged

### Avoid

- frontend-only bypass checks
- query-string shortcuts such as `?skipBilling=1`
- hidden email-domain magic as the primary control

## Billing State Model

Recommended account billing states:

- `unconfigured` - no subscription or override yet
- `checkout_required` - user must complete Checkout before launch
- `active` - paid and in good standing
- `past_due` - payment failed; restrict new launches and prompt for payment update
- `canceled` - no active paid entitlement
- `internal` - admin bypass
- `tester` - tester bypass
- `comped` - customer comped by admin

## Backend Enforcement Points

Billing enforcement should happen on backend actions that allocate value:

- create Machine if you want to cap dormant inventory
- launch/start Machine if you want to defer billing until real usage
- update Machine size if size affects price

Recommended v1 rule:

- allow draft Machine creation
- require billing eligibility at `Launch Machine`

Reasoning:

- preserves a smooth setup flow
- charges only when the user is ready to provision value
- matches the current UX where launch is the meaningful commitment point

## Stripe Responsibilities

Stripe should manage:

- customer record
- subscription
- quantity billing
- payment method collection
- invoices
- prorations
- taxes
- customer portal

The app should manage:

- which Machine actions are allowed
- mapping account Machine count to subscription quantity
- account-specific overrides

## Webhook-Driven Actions

Minimum webhook set:

- `checkout.session.completed`
- `customer.subscription.updated`
- `customer.subscription.deleted`
- `invoice.paid`
- `invoice.payment_failed`

Recommended behavior:

- successful payment activates launch entitlement
- payment failure blocks new paid launches and prompts payment recovery
- cancellation removes paid entitlement at period end unless bypass exists

## Copy Guidelines

Use simple product language:

- say `Machine`, not `seat`
- say `billed monthly per Machine`
- say `launch discount` or `introductory discount`, not `coupon code`
- say `manage billing`, not `manage subscription quantity`

## Open Questions

- should v1 use flat per-Machine pricing or size-based pricing?
- should deleting a Machine reduce quantity immediately or next cycle?
- should dormant stopped Machines count forever, or should there be a future low-cost archived state?
- should the introductory discount be a fixed dollar amount or percentage off the first invoice?

## Recommended v1

If the goal is simplicity, ship this:

- monthly billing per Machine
- payment required at first launch
- automatic first-invoice discount for the first Machine
- quantity increases when additional Machines are launched
- deletion reduces billed quantity on the next cycle
- stop/start does not affect billing
- admin/tester accounts use explicit backend billing overrides

