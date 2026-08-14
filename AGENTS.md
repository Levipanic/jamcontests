# AGENTS.md

## Communication

- Always answer the user in Russian.
- Keep reports factual and concise. State what changed, what was verified, and what could not be verified.

## Product Contract

- Treat this product as a platform for universal team creative jams, not a literature-only service. A result may be text, a game, music, video, illustration, an interactive work, or another medium.
- A team belongs to one jam and may have at most one product in that jam.
- Store only the product card and external URLs. Never add hosting for result files; availability and content of the external resource are outside the platform.
- Preserve this priority order: correctness and security, simplicity, readability, development speed, maintainability.
- Implement the smallest correct solution for the current requirement. Do not design for hypothetical scale or speculative future features.

## Required Stack And Architecture

- Use the existing stack exactly: Go, Gin, PostgreSQL, pgx, `html/template`, vanilla JavaScript, and plain CSS.
- Keep the application a modular monolith organized around actual product domains such as auth, users, jams, teams, questionnaires, themes, products, nominations, voting, bumps, and admin. Existing repository structure takes precedence; do not reorganize it merely to fit an ideal layout.
- Prefer the direct request flow: Gin handler, domain logic only when needed, explicit SQL through pgx, PostgreSQL, then server-rendered HTML, redirect, or a small JSON response.
- Keep handlers simple. Extract logic only when it is genuinely complex or reused. Do not create interfaces, repositories, service layers, or generic abstractions for a single implementation.
- Render the main UI with `html/template`. For ordinary forms prefer `POST -> validation -> transaction -> redirect -> GET`.
- Use small `fetch` endpoints only where they materially improve UX, including questionnaire autosave, bumps, voting, live counters, and focused admin actions.
- Do not introduce SPA architecture, React, Vue, Next.js, HTMX, Alpine.js, Tailwind, an ORM, Redis, Kafka, microservices, Kubernetes, CQRS, event sourcing, an event bus, generic repository layers, or DI frameworks without an explicit current requirement.
- Add a focused dependency only when the standard library cannot solve a fundamental concern clearly and safely, such as password hashing or migrations. Do not add infrastructure or dependencies speculatively.

## Jam Visibility And Effective Stage

- Model public visibility and lifecycle separately. `visibility` is `draft` or `published`; effective stage is exactly `upcoming`, `submission`, `evaluation`, `voting`, or `finished`.
- Never expose a draft to normal users, regardless of its time-derived stage.
- Allow at most one published active jam at a time. Enforce this in server logic and, where practical, PostgreSQL constraints. The archive contains published jams whose effective stage is `finished`.
- Compute the canonical effective stage on the server for each stage-sensitive operation. Use an explicit admin status override when present; otherwise calculate from server time and schedule boundaries.
- Do not use cron merely to update a status column. Browser timers are display-only and must never authorize or switch canonical state.
- Accept admin date input and display dates in `Europe/Moscow`. Store instants in PostgreSQL as `timestamptz`; convert between Moscow local time and absolute instants explicitly and consistently.
- Recheck visibility, effective stage, deadlines, role, membership, ownership, and other permissions on the backend at mutation time. Never derive authority from hidden inputs, client IDs, disabled controls, or JavaScript state.

## Stage Rules And Disclosure

- In `upcoming`, authenticated users may create teams and join by invitation; team members complete the individual eligibility questionnaire. Keep themes, other teams' products, and theme selections hidden.
- In `submission`, reveal prepared themes automatically; allow the captain to choose or change the team's theme; allow authorized product editing and final submission; allow users to create or join teams and make other self-service membership changes within team rules through the end of this stage.
- In `evaluation`, reveal submitted products and their selected themes; allow registered users to bump products; do not allow nomination voting.
- In `voting`, keep products and bumps available; reveal nomination titles and curator marks while keeping team-nomination authorship hidden; allow registered users to vote by nomination; expose current vote counts in real time.
- In `finished`, close voting and bumps, publish nomination results, reveal team-nomination authorship, and keep the published jam in the archive.
- Guests may view only published jams and content already public for the effective stage. Require an account for team actions, questionnaire answers, theme selection, product editing, bumps, and voting.
- Apply disclosure rules to every surface: HTML, JSON, counters, sorting, aggregates, search, exports, metadata, error messages, internal-looking public routes, and sequential identifiers. Do not leak products, selected themes, nominations, authorship, vote state, or results before their allowed stage.
- Make frontend counters and timers presentation only. PostgreSQL and backend checks remain authoritative.

## Teams And Invitations

- A team has a required name, one captain, members, an optional description, and an optional uploaded avatar.
- Enforce case-insensitive team-name uniqueness within each jam. The create form includes name, optional description, and optional avatar.
- Enforce at most one team membership per user per jam at the database level and in transactional server logic.
- Store a per-jam maximum team size. Enforce it on the backend with locking or an equivalent concurrency-safe operation; a check followed by an unprotected insert is insufficient.
- Only the captain may edit the team profile, issue or revoke an invite, appoint product editors, and transfer captaincy.
- Generate invite tokens cryptographically randomly and unpredictably. Make them revocable and store only a safe representation where practical. Revocation must invalidate the previous link immediately; never log the raw token.
- If a user already belongs to another team in the jam, reject a foreign invite clearly. Never auto-leave or auto-switch the user.
- Require a captain to transfer captaincy before leaving. Permit self-service membership changes only through the end of `submission`.
- Permit an admin to intervene in membership or captaincy at any stage, but require an explicit reason and immutable audit record.
- Store team avatars on persistent local VPS storage and include them in backups. Enforce server-side size and allowed-format limits, inspect actual content type rather than trusting extension or headers, generate safe filenames, prevent execution, and keep uploads outside executable paths.
- Team detail shows avatar, name, description, captain, and members by public username. Show management actions only to the current team as role/stage permit; keep questionnaire answers, invite links, editor powers, themes, and products subject to their privacy and disclosure rules.

## Eligibility Questionnaire

- Maintain exactly one questionnaire per jam. Create its empty shell atomically with the draft jam, require at least one configured question before publication, and make it available in `upcoming` only to that jam's team members and admins.
- Support exactly short text, single choice, and multiple choice questions.
- Model each question with `prompt`, optional `hint`, required flag, text-length limit where applicable, multiple-choice selection limit where applicable, and position.
- Store answers per user, never per team. Autosave a draft through small CSRF-protected `fetch` requests.
- Require an explicit `Complete` action and perform full server validation of required answers and all limits. Editing a completed response before `submission` returns it to draft and requires completion again.
- Consider a team eligible when at least one current member has a completed response or an admin has set an audited eligibility override.
- Preserve answers and history when a member leaves, but stop counting that former member toward team eligibility. Do not infer an override automatically.
- Never publish questionnaire answers. Team members may see only other members' completion statuses, not their answers.
- Give admins completion summaries, individual answers, filtering by team, and CSV export.
- Protect CSV exports from formula injection and preserve correct encoding and quoting. Treat cells beginning with formula-triggering characters as untrusted spreadsheet input.
- After the first answer exists, permit only edits that cannot change the meaning or validity of stored answers. Require an explicit confirmed reset with immutable audit for destructive structural changes. Retain answers and history after the jam ends.

## Themes

- A theme belongs to one jam and is only a simple phrase or word. Do not add a general theme lifecycle, hidden curator notes, or unrelated metadata. If an exposed theme must be withdrawn, retain the historical row with minimal withdrawal metadata rather than hard-deleting it.
- Require at least one theme before `submission` starts. If time advances without one, do not falsify the effective stage; report a critical configuration error and block theme selection and final submission until an admin fixes it. Validate theme presence before direct overrides into `submission` or later and dangerous schedule changes.
- Reveal themes automatically at `submission`. Allow only the captain to select one theme for the team and change it through the end of `submission`; multiple teams may select the same theme.
- Keep each team's selection hidden from outsiders until `evaluation`.
- Require a selected theme for final product submission. Do not silently delete or mutate a selected theme in a way that destroys the historical meaning of a submission; require explicit safe reassignment before withdrawing a selected theme.
- Audit emergency changes after reveal and every administrative theme-selection intervention.
- Archive themes with the completed jam. Reuse a theme by copying it into an independent record for the new jam, never through a shared mutable record.

## Products

- Enforce at most one product per team per jam.
- The product card contains exactly: required title, required external result URL, optional description, optional external commentary or review URL, and optional notes.
- Store product metadata and external URLs only. Do not upload or proxy result files.
- Validate all URLs server-side. Allow only absolute `http` and `https` URLs; reject embedded credentials, control characters, malformed values, and ambiguous forms.
- Allow the captain and captain-appointed current team members acting as product editors to create and edit the card through the end of `submission`.
- Derive the team and permissions from the authenticated server session and current membership. Never trust browser-supplied `user_id`, `team_id`, author, or editor status.
- Require title, result URL, and selected theme for final submission.
- Do not reveal another team's product or selected theme before `evaluation`, including through existence checks, counts, errors, APIs, and identifiers.

## Nominations And Voting

- Allow a team to propose at most one optional nomination with its product.
- Allow admins to add any number of curator nominations before `voting` starts. Reveal nomination titles and curator marks at `voting`, not earlier.
- Hide the authoring team of a team nomination until `finished`; reveal it after finishing.
- During `voting`, allow every registered user at most one selected product per nomination. The user may change that selection until voting closes.
- Never allow a user to vote for a product of their own current team.
- Verify authentication, effective stage, nomination and product ownership by the jam, membership, and all vote limits on the backend.
- Enforce one active selection per user per nomination with PostgreSQL uniqueness and transactions so concurrent requests cannot create duplicates.
- Show current vote counts in real time during `voting`, but calculate authoritative counts on the backend.
- If several products share the maximum count, all are winners of that nomination. Do not add a tie-breaker, an overall product ranking, or an overall jam winner.
- Do not expose hidden vote totals or results before the allowed stage through counts, ordering, aggregates, or errors.

## Bumps

- Treat bumps as reactions separate from nominations, votes, and results.
- Allow any registered user to bump a product repeatedly during `evaluation` and `voting`, with a one-minute cooldown for each user-product pair.
- Show an up-to-date bump counter during `evaluation` and `voting`; preserve the final public counter in the `finished` archive. Close bump mutations outside `evaluation` and `voting`.
- Do not prohibit bumping the user's own team product unless the product requirements explicitly change. Never copy the voting self-selection ban into bump logic by assumption.
- Validate authentication, effective stage, product existence, product-jam relationship, and cooldown on the backend.
- Make cooldown enforcement concurrency-safe with locking, an atomic statement, or an equivalent PostgreSQL guarantee. Never rely on an unprotected `SELECT` followed by `INSERT`.

## Admin And Immutable Audit

- Protect all admin routes and actions server-side with the `admin` role.
- Provide admin control over jams, visibility, schedules, status overrides, complete team profiles, avatars, invites, memberships, captains, eligibility overrides, themes and selections, products and moderation, nominations, votes, bumps, users, and roles.
- Allow admins to change deadlines, set an explicit effective-stage override, and return a jam to automatic stage calculation.
- Keep dangerous actions explicit, visibly distinct, confirmation-gated, and unable to bypass product invariants silently.
- Append an immutable audit entry for every admin action affecting lifecycle, access, membership, user content, or results, including ordinary create and edit operations in those domains.
- Each audit entry must identify the admin, action, affected entity, timestamp, required reason, and material before/after values.
- Audit at minimum overrides, deadline changes, post-reveal changes, membership and captain changes, eligibility overrides, theme interventions, product moderation, nomination interventions, vote and bump interventions, and user or role management.
- Do not hard-delete published or finished jams or their historical questionnaire, theme, product, nomination, voting, bump, or audit data.

## Authentication And Security

- Require `username` and password for registration; email is optional. Authenticate by username, not email.
- Treat username as public, editable, and case-insensitively unique. Treat optional email as private to its owner and admins and case-insensitively unique when present.
- Let the profile display and edit username and optional email with server-side uniqueness checks. Do not add email verification, password recovery, or an email service.
- Use only server-side sessions. Put a cryptographically random token in a cookie and store only a secure hashed representation in the database where practical.
- Set session cookies `HttpOnly`, use an appropriate `SameSite` policy, and set `Secure` in production. Do not use JWTs in `localStorage`.
- Hash passwords with a modern password-hashing algorithm.
- Create the first admin through an application CLI command. Never expose public self-promotion to `admin`.
- Apply CSRF protection from the first state-changing form and to every state-changing `fetch` request. `SameSite` is not sufficient by itself.
- Validate authentication, role, ownership, membership, stage, visibility, identifiers, lengths, formats, limits, and deadlines on the backend. Client validation exists only for UX.
- Rely on `html/template` escaping for user text. Do not mark user content as trusted HTML without an explicit rich-text requirement, robust sanitization, and a threat model.
- Validate external URLs by safe allowed schemes. Apply the upload controls defined above to avatars.
- Neutralize spreadsheet formula injection in every CSV export containing untrusted values.
- Return understandable errors without revealing hidden data or internals.
- Never log passwords, raw session tokens, invitation tokens, recovery tokens, CSRF tokens, credentials, or other secrets. Avoid placing secrets in error context, audit before/after data, metrics, or URLs.

## PostgreSQL Integrity And Concurrency

- Treat PostgreSQL as the source of truth and use explicit SQL through pgx; do not add an ORM.
- Encode critical invariants with `NOT NULL`, `FOREIGN KEY`, `UNIQUE`, partial unique indexes, and `CHECK` constraints wherever PostgreSQL can enforce them.
- Protect at minimum one membership per user per jam, one product per team per jam, one questionnaire per jam, one active vote selection per user per nomination, and other representable uniqueness rules.
- Use transactions for logically related writes. Use row/advisory locking, atomic statements, or equivalent database guarantees for max team size, votes, bump cooldown, and every check-then-write race.
- Never rely only on frontend validation or a sequential unprotected `SELECT` then `INSERT` for a concurrent invariant.
- Represent every schema change with a small, focused, understandable migration. Never make undocumented manual production changes or combine unrelated schema work.
- Protect the audit log as append-only with restricted PostgreSQL permissions for the application role or an equivalent verifiable database-level mechanism, not merely by omitting edit UI.

## UI Guidance

- Preserve the restrained noir archival-dossier visual language: case folders, paper documents, typewriter typography, stamps, archive numbers, photographs, red annotations, subtle grain, and restrained tension.
- Keep the homepage public. Once per browser session, auto-open a closable auth dossier using `sessionStorage`, default it to login, and reopen it when a guest attempts a protected action; this is UX only, never authorization.
- Use a fixed left anchor navigation on desktop and fixed bottom navigation on mobile. Include a stage ribbon with a display-only timer, horizontal folder-style team cards with the user's own team first, and a profile section.
- Never render an admin UI link, including for admins. Admin access is only through the directly entered, server-protected `/admin` route.
- Prioritize readability over atmosphere and clarity over effects in admin screens. Make dangerous actions unmistakable.
- Keep animation subtle, prefer CSS, and respect `prefers-reduced-motion`. Do not use JavaScript for effects CSS can implement reliably.

## Task-By-Task Discipline

- Work from the current user request and `context.md`; treat current repository behavior and conventions as implementation evidence, not permission to retain stale product rules.
- Before editing, inspect affected files, routes, schema, migrations, tests, and conventions. Identify domain invariants, permissions, stage gates, disclosure boundaries, and race conditions.
- Maintain a task-specific backlog or checklist for non-trivial work. Complete items in dependency order and update the list as facts emerge; do not broaden it with unrelated cleanup or speculative follow-ups.
- Choose the smallest localized change that fully satisfies the task. Preserve existing style and do not redesign unrelated code.
- Respect a dirty worktree. Never revert, overwrite, stage, or alter unrelated user or agent changes. If an unexpected change directly conflicts with the task, stop and ask; otherwise leave it untouched.
- Do not create commits, amend commits, push, or open pull requests unless the user explicitly requests it.
- Add tests for meaningful business logic and security-sensitive behavior, especially stage calculation and override, deadlines, authorization, membership and size limits, questionnaire eligibility, hidden-data disclosure, product editing, voting and ties, bump cooldown, concurrency, and token handling.
- After code changes, run `gofmt -w .`, `go test ./...`, and `go build ./...`. Also inspect migrations and exercise affected user scenarios when applicable.
- Report every verification that could not run and every failure. Never claim success while tests or build fail.

## Definition Of Done

A task is complete only when all applicable conditions hold:

- The requested behavior is implemented without unrelated scope expansion.
- Behavior matches jam visibility and effective-stage rules.
- Backend authorization, ownership, membership, deadlines, limits, and validation are correct.
- Hidden information cannot escape before its disclosure stage on any response surface.
- PostgreSQL constraints and transactions preserve critical invariants under concurrent requests.
- Every schema change has an appropriate migration.
- Required administrative actions create immutable audit records with reason and material before/after values.
- User-facing errors are clear, user text is escaped, exports and uploads are safe, and no secrets reach responses or logs.
- Significant logic and security-sensitive paths have relevant tests.
- Go code is formatted, `go test ./...` passes, and `go build ./...` passes, or unavailable checks are explicitly reported.
- No unnecessary dependency, framework, abstraction, distributed component, or speculative compatibility code was added.
- Unrelated worktree changes remain untouched, and no commit was created unless explicitly requested.
