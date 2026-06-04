# Authentication Sequence Diagrams

Date: 2026-03-26
Status: Proposed

This document separates the authentication flows from the broader design discussion in [docs/design/2026-03-26-login-architecture-cloudflare-okta-firebase.md](./design/2026-03-26-login-architecture-cloudflare-okta-firebase.md).

It shows:

- `Current state`: Cloudflare Access as the customer-facing browser gate
- `Target state`: Firebase for customer auth, Cloudflare retained for admin/internal surfaces

## Current State

### Current Flow 1: Customer browser login via Cloudflare Access

```mermaid
sequenceDiagram
    participant Browser as Customer Browser
    participant Access as Cloudflare Access
    participant Frontend as OCM Frontend
    participant Backend as OCM Backend
    participant DB as Postgres

    Browser->>Access: Open /dashboard
    Access-->>Browser: Require login if no session
    Browser->>Access: Complete Access login
    Access-->>Browser: Set CF_Authorization
    Access-->>Frontend: Authenticated page request

    Frontend->>Backend: GET /api/auth/me
    Note over Frontend,Backend: Frontend forwards Cf-Access-Jwt-Assertion

    Backend->>Backend: Validate Access JWT
    Backend->>DB: Lookup user by cf_sub
    alt Existing user
        DB-->>Backend: User found
    else First login
        DB-->>Backend: No user found
        Backend->>DB: Lookup by email
        DB-->>Backend: No user found
        Backend->>DB: Create user + personal account
        DB-->>Backend: User/account created
    end

    Backend-->>Browser: Set ocm_token
    Backend-->>Frontend: 200 { user }
    Frontend-->>Browser: Render /dashboard
```

### Current Flow 2: Current CLI login via Cloudflare Access

```mermaid
sequenceDiagram
    participant CLI as ocm login
    participant Browser as Browser
    participant Access as Cloudflare Access
    participant Frontend as /cli-auth
    participant Backend as OCM Backend

    CLI->>Browser: Open /cli-auth?port=PORT
    Browser->>Access: Request protected path
    Access-->>Browser: Require login if needed
    Browser->>Access: Complete login
    Access-->>Frontend: Authenticated request

    Frontend->>Backend: POST /api/auth/cli-token
    Note over Frontend,Backend: Request already authenticated via Access
    Backend-->>Frontend: 200 { token }
    Frontend-->>Browser: Redirect to localhost callback with token
    Browser-->>CLI: Deliver token to callback server
    CLI->>Backend: Validate token with /api/auth/me
    Backend-->>CLI: 200 OK
```

### Current Flow 3: Current machine-route access

```mermaid
sequenceDiagram
    participant Browser as Customer Browser
    participant Worker as Cloudflare Worker
    participant KV as Cloudflare KV
    participant Backend as OCM Backend
    participant Agent as Host Agent
    participant VM as Machine Service

    Browser->>Worker: GET https://{account}.openclawmachines.com/{machine}/...
    Note over Browser,Worker: Browser sends ocm_token cookie

    Worker->>Worker: Validate ocm_token
    Worker->>KV: Lookup route:{account}:{machine}
    alt KV hit
        KV-->>Worker: host + machine_id + proxy token
    else KV miss
        Worker->>Backend: POST /api/internal/resolve
        Backend-->>Worker: host + machine_id + proxy token
    end

    Worker->>Agent: Forward request with X-Proxy-Token
    Agent->>VM: Proxy to target service
    VM-->>Agent: Response
    Agent-->>Worker: Response
    Worker-->>Browser: Response
```

## Target State

### Target Flow 1: First-time customer login via Firebase

```mermaid
sequenceDiagram
    participant Browser as Customer Browser
    participant Frontend as OCM Frontend
    participant Firebase as Firebase Auth
    participant Backend as OCM Backend
    participant DB as Postgres

    Browser->>Frontend: Open /login
    Frontend->>Firebase: Start sign-in flow
    Firebase-->>Browser: Hosted/custom sign-in UI
    Browser->>Firebase: Complete login
    Firebase-->>Frontend: ID token + profile

    Frontend->>Backend: POST /api/auth/session/exchange
    Note over Frontend,Backend: Body contains Firebase ID token

    Backend->>Backend: Verify Firebase token
    Backend->>DB: Lookup user by identity_issuer + identity_subject
    DB-->>Backend: No user found
    Backend->>DB: Lookup by email
    DB-->>Backend: No user found
    Backend->>DB: Create user + personal account
    DB-->>Backend: User/account created

    Backend-->>Browser: Set app session cookie + Set ocm_token
    Backend-->>Frontend: 200 { user, accounts }
    Frontend-->>Browser: Redirect to /welcome or /dashboard
```

### Target Flow 2: Returning customer request with existing session

```mermaid
sequenceDiagram
    participant Browser as Customer Browser
    participant Frontend as OCM Frontend
    participant Backend as OCM Backend
    participant DB as Postgres

    Browser->>Frontend: Open /dashboard
    Frontend->>Backend: GET /api/auth/me
    Note over Frontend,Backend: Browser sends app session cookie

    Backend->>Backend: Validate app session
    Backend->>DB: Load user + accounts
    DB-->>Backend: User/account records
    Backend-->>Frontend: 200 { user }
    Frontend-->>Browser: Render dashboard
```

### Target Flow 3: Customer machine-route access after Firebase login

```mermaid
sequenceDiagram
    participant Browser as Customer Browser
    participant Worker as Cloudflare Worker
    participant KV as Cloudflare KV
    participant Backend as OCM Backend
    participant Agent as Host Agent
    participant VM as Machine Service

    Browser->>Worker: GET https://{account}.openclawmachines.com/{machine}/...
    Note over Browser,Worker: Browser sends ocm_token cookie set by OCM backend

    Worker->>Worker: Validate ocm_token
    Worker->>KV: Lookup route:{account}:{machine}
    alt KV hit
        KV-->>Worker: host + machine_id + proxy token
    else KV miss
        Worker->>Backend: POST /api/internal/resolve
        Backend-->>Worker: host + machine_id + proxy token
    end

    Worker->>Agent: Forward request with X-Proxy-Token
    Agent->>VM: Proxy to target service
    VM-->>Agent: Response
    Agent-->>Worker: Response
    Worker-->>Browser: Response
```

### Target Flow 4: Customer logout

```mermaid
sequenceDiagram
    participant Browser as Customer Browser
    participant Frontend as OCM Frontend
    participant Firebase as Firebase Auth
    participant Backend as OCM Backend

    Browser->>Frontend: Click "Log out"
    Frontend->>Backend: POST /api/auth/logout
    Backend-->>Browser: Clear app session cookie + clear ocm_token
    Frontend->>Firebase: Sign out current Firebase session
    Firebase-->>Frontend: Sign-out complete
    Frontend-->>Browser: Redirect to /
```

### Target Flow 5: Admin or internal user login via Cloudflare Access

```mermaid
sequenceDiagram
    participant Browser as Admin Browser
    participant Access as Cloudflare Access
    participant IdP as Okta or Google Workspace
    participant Backend as OCM Backend
    participant DB as Postgres

    Browser->>Access: Open protected admin route
    Access->>IdP: Redirect to upstream IdP
    IdP-->>Access: Successful login
    Access-->>Browser: Set CF_Authorization
    Access->>Backend: Forward request with Access JWT

    Backend->>Backend: Validate Access JWT
    Backend->>DB: Resolve admin user by identity
    DB-->>Backend: User found or created
    Backend-->>Browser: Admin page response
```

### Target Flow 6: Admin CLI login through Cloudflare path

```mermaid
sequenceDiagram
    participant CLI as ocm login
    participant Browser as Browser
    participant Access as Cloudflare Access
    participant Frontend as /cli-auth
    participant Backend as OCM Backend

    CLI->>Browser: Open /cli-auth?port=PORT
    Browser->>Access: Request protected path
    Access-->>Browser: Require login if needed
    Browser->>Access: Complete admin login
    Access-->>Frontend: Authenticated request

    Frontend->>Backend: POST /api/auth/cli-token
    Note over Frontend,Backend: Request is already authenticated by Access
    Backend-->>Frontend: 200 { token }
    Frontend-->>Browser: Redirect to localhost callback with token
    Browser-->>CLI: Return token to local callback server
    CLI->>Backend: Validate token with /api/auth/me
    Backend-->>CLI: 200 OK
```

## Current vs Target Delta

### What changes

- customer login moves from `Cloudflare Access` to `Firebase`
- OCM backend becomes the explicit session exchange boundary
- OCM app session becomes first-party and app-managed
- user identity storage should move from `cf_sub` to provider-neutral identity keys

### What stays the same

- `ocm_token` remains the Worker-facing token
- Worker route resolution remains KV plus `/api/internal/resolve`
- per-machine proxy tokens remain unchanged
- Cloudflare still protects admin/internal routes and infrastructure surfaces
