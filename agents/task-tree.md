# here.now — Launch Dependency Tree

> Dependency graph and critical path for the launch tasks in [tasks.md](tasks.md).
> A node requires everything that points into it (arrows flow toward LAUNCH).

## ASCII tree

```
                          ┌─────────────────────── LAUNCH ───────────────────────┐
                          │                                                       │
                     [G2 e2e] [G1 unit] [G5 sec review]        [F5 deploy docs]
                          │        │          │                       │
        ┌─────────────────┼────────┴───────┐  │             ┌─────────┴─────────┐
        │                 │                 │  │             │                   │
   RENDER PARITY      SHARING/RBAC      DASHBOARD        [F1 compose]        [F2 TLS]
   ┌────┴────┐        ┌───┴────┐        ┌───┴───┐             │                   │
 [A1 harness]      [C2 share] [C3 UI] [D2 board][D3 login]  [F4 env config]───────┘
   │   │            │    │        │      │  │
 [A3 CSP][A2 origin][C1 publish API]  [D1 ListVisibleTo]
                     │                   │
             ┌───────┴────────┐          │
        [B3 CLI-as-client]    │          │
          │        │          │          │
     [B2 CLI OIDC][B1 browser OIDC]──────┴──────► (auth underpins C3 / D2 / D3)
                     │
              (OIDC issuer configured — infra prerequisite)

  Independent / do-anytime: E1 stream · E2 atomic writes · G4 limits · F3 metrics · G6 audit verify
```

## Mermaid

```mermaid
graph TD
  B1[B1 OIDC browser] --> B3[B3 CLI-as-client]
  B2[B2 OIDC CLI device flow] --> B3
  B1 --> D3[D3 login page]
  B1 --> D2[D2 dashboard]
  B3 --> C1[C1 publish API]
  C1 --> C2[C2 artifacta share]
  D1[D1 ListVisibleTo] --> D2
  C2 --> C3[C3 Share UI]
  D2 --> C3
  A1[A1 render harness] --> A3[A3 CSP]
  A2[A2 separate origin] --> A3
  F4[F4 env config] --> F1[F1 prod compose]
  F1 --> F2[F2 TLS]

  A3 --> G2[G2 e2e]
  C2 --> G2
  D2 --> G2
  G1[G1 unit tests] --> LAUNCH
  G2 --> LAUNCH
  G5[G5 security review] --> LAUNCH
  F2 --> LAUNCH
  F5[F5 deploy docs] --> LAUNCH
  C3 --> LAUNCH
  A3 --> LAUNCH
```

## Critical path (team launch)

Longest chain to launch:

```
B1 OIDC browser → B3 CLI-as-client → C1 publish API → C2 share → C3 Share UI → G2 e2e → LAUNCH
```

…run in parallel with the independent long pole:

```
A1 render parity → A3 CSP → G2 e2e → LAUNCH
```

The schedule is gated by two parallel tracks — the **auth → sharing chain (B → C)** and the
**render-parity chain (A)**. Deploy (F) and tests (G) sit at the bottom and are built alongside
once features exist. Starting A1 and B1 simultaneously compresses the calendar the most.

## Minimal single-user bar

Delete Scopes B and C entirely plus D2/D3. Critical path collapses to:

```
A1 render parity → A3 CSP → F1/F2 deploy + TLS → G1/G2/G5 tests + sec review → LAUNCH
```

This is the ~2–4 week version.
