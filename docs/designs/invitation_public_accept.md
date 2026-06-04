# Design: Public Invitation Accept Flow

**Status**: Proposed
**Date**: 2026-03-14
**Problem**: Invited users who are not yet in the CF Access allow policy cannot see the invitation page or sign up. CF Access blocks them at the edge before they ever reach the app.

## Problem Statement

When an existing user invites someone via email, the invitation link points to `https://openclawmachines.com/invitations/{token}`. This URL is behind Cloudflare Access. If the invited person's email/domain is not in the CF Access allow policy, they are blocked at the edge — they never see a login page, signup form, or any app UI. The flow is completely broken for new users outside the configured CF Access policy.

## Current Flow (Broken for New Users)

```
Invited user clicks email link
  → openclawmachines.com/invitations/{token}
  → CF Access intercepts (no valid cookie)
  → CF Access checks email against allow policy
  → ❌ Email not in policy → "Access denied" page (no login form)
  → User is stuck. Cannot proceed.
```

## Proposed Solution

Split the invitation flow into two phases: a **public landing page** that shows invitation details without authentication, and a **protected accept action** that requires CF Access login.

### Design Principles

1. **Invitation tokens are already secrets** — the token in the URL is unguessable (UUID/random). Knowing the token IS the authorization to view invitation metadata.
2. **Viewing ≠ accepting** — showing "You've been invited to join X as a member" leaks nothing sensitive beyond what the email already told them.
3. **Accept still requires auth** — the email-matching check on accept remains the security gate.

### New Flow

```
Invited user clicks email link
  → openclawmachines.com/invitations/{token}
  → Frontend loads (public route, no CF Access needed)
  → Frontend fetches GET /api/invitations/{token}/public (no auth)
  → Shows invitation details: account name, role, inviter
  → User clicks "Accept Invitation"
  → Frontend redirects to CF Access login
  → User authenticates (signs up if needed)
  → Frontend calls POST /api/invitations/{token}/accept (authenticated)
  → Backend validates email match, creates membership
  → Redirect to dashboard
```

## Changes Required

### 1. Backend: New Public Endpoint

**Add** `GET /api/invitations/{token}/public` — outside the auth middleware group.

```go
// Public routes (no auth required)
r.Get("/api/invitations/{token}/public", srv.handleGetInvitationPublic)
```

**Response** (same as existing `handleGetInvitation` but always redacts emails):

```json
{
  "account_name": "Acme Corp",
  "role": "member",
  "status": "pending",
  "expires_at": "2026-03-21T00:00:00Z"
}
```

Fields deliberately excluded from public response:
- `id` — internal, not needed
- `account_id` — internal, not needed for display
- `email` — the invited email (user already knows from their inbox)
- `inviter_email` — only shown after auth confirms identity

**Security**: The token is a 128-bit random string. Knowing the token already proves the user received the email. The public endpoint reveals only: account name, role, invitation status, and expiry — all of which were already in the invitation email body.

### 2. Backend: Move Auth-Required Endpoints

Keep existing authenticated endpoints unchanged:
- `GET /api/invitations/{token}` — full details (with email fields) for authenticated users
- `POST /api/invitations/{token}/accept` — requires auth + email match
- `POST /api/invitations/{token}/decline` — requires auth

### 3. Frontend: Split InvitationAccept into Two Phases

**Phase 1 — Public View** (no auth required):
- Remove `<ProtectedRoute>` wrapper from `/invitations/:token` route
- On mount, call `GET /api/invitations/{token}/public`
- Display: account name, role, expiry
- Show "Accept Invitation" and "Decline" buttons

**Phase 2 — Auth-gated Accept**:
- When user clicks "Accept Invitation":
  - Check if user is already authenticated (`useAuth()`)
  - If yes: call `POST /api/invitations/{token}/accept` directly
  - If no: trigger CF Access login flow, then accept on return

**CF Access Login Trigger**:
The frontend needs to redirect to CF Access login and return to the invitation page afterward. The approach:

```typescript
// If not authenticated, redirect to a protected route that bounces back
if (!user) {
  // Store the token so we can resume after auth
  sessionStorage.setItem("ocm_pending_invitation", token);
  // Navigate to dashboard (behind CF Access) — after login,
  // ProtectedRoute will check for pending invitation and redirect back
  window.location.href = "/dashboard";
  return;
}
```

Or simpler: just reload the current page after clearing CF cookies — same as `ProtectedRoute` does today, but only triggered on button click:

```typescript
if (!user) {
  // Clear stale CF cookies and reload — CF Access will intercept
  clearCfCookies();
  window.location.reload();
  return;
}
```

**Recommended approach**: Use a dedicated redirect. Store the invitation token in `sessionStorage`, redirect to `/dashboard` (which triggers CF Access login), then check on the dashboard load if there's a pending invitation and redirect back to `/invitations/{token}`.

### 4. Frontend: Route Change

```tsx
// Before (requires auth to even see the page)
<Route path="/invitations/:token" element={
  <ProtectedRoute><InvitationAccept /></ProtectedRoute>
} />

// After (public page, auth only on accept)
<Route path="/invitations/:token" element={
  <InvitationAccept />
} />
```

### 5. Cloudflare Access: Bypass Rule

Add a bypass rule in the CF Access application policy for the invitation public endpoint:
- Path: `/api/invitations/*/public`
- Action: Bypass (no authentication required)

The frontend HTML/JS is already served from a public Cloud Run service, so the SPA loads without CF Access. Only the API call needs the bypass.

Alternatively, if the Cloudflare Worker handles routing, add a bypass in the Worker for this specific path pattern.

### 6. Rate Limiting (Optional, Recommended)

Since the public endpoint doesn't require auth, add basic rate limiting to prevent enumeration:
- Rate limit `GET /api/invitations/{token}/public` to 10 req/min per IP
- Return 404 (not 400) for invalid tokens to avoid leaking token format

## Security Analysis

| Threat | Mitigation |
|--------|-----------|
| Token enumeration/brute-force | Tokens are 128-bit random UUIDs — infeasible to guess. Rate limiting adds defense-in-depth. |
| Information disclosure via public endpoint | Only reveals account name, role, status, expiry — all already in the email body. No emails, IDs, or internal data. |
| Link forwarding (user A accepts invitation meant for user B) | Email matching on `POST /accept` is unchanged — backend compares CF Access JWT email with invitation email (case-insensitive). |
| Replay after accept | `AcceptInvitation` uses row-level lock + status check in transaction. Only pending invitations can be accepted. |
| CSRF on accept | `POST /accept` requires CF Access JWT, which is not automatically sent cross-origin. |

## Alternatives Considered

### A. Widen CF Access Policy to "Everyone"
- Pros: No code changes
- Cons: Opens the entire app to any email, defeats the purpose of CF Access as a gatekeeper. Cannot restrict access to specific organizations.

### B. Add Invited Emails to CF Access Policy Automatically
- Pros: Maintains CF Access as gatekeeper
- Cons: Requires Cloudflare API integration to manage Access policies. Complex, adds latency to invitation creation, and CF Access group changes can take minutes to propagate.

### C. One-Time-Use Magic Link (Token = Auth)
- Pros: Simplest UX — click link, you're in
- Cons: No email verification. Anyone who intercepts the link can join. Weaker security than email-matching.

**Chosen: Public landing page + auth-gated accept** — best balance of UX (works for anyone with the link) and security (email matching on accept).

## Implementation Checklist

- [ ] Add `handleGetInvitationPublic` endpoint (backend)
- [ ] Register public route outside auth middleware group
- [ ] Update `InvitationAccept.tsx` to use public endpoint on mount
- [ ] Remove `<ProtectedRoute>` from invitation route in `App.tsx`
- [ ] Add auth-triggering flow when user clicks Accept without being logged in
- [ ] Add CF Access bypass rule for `/api/invitations/*/public`
- [ ] Add rate limiting to public endpoint (optional)
- [ ] Update tests for new public endpoint
- [ ] Test end-to-end with a user not in CF Access policy
