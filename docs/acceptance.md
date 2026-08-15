# Acceptance

This document is the pre-release acceptance checklist for Jam Contests. Most of
it is covered by automated tests (see "Automated coverage"); the manual steps
exercise the real browser, filesystem, and process environment that the test
suite cannot fully reproduce.

## Automated coverage

Run the full suite against a disposable database before every release:

```sh
TEST_DATABASE_URL='postgres://...?sslmode=disable' go test ./... -race -count=1
go vet ./... && go build ./...
```

Key integration tests:

| Test | Covers |
|------|--------|
| `TestEndToEndJamJourneyRealUsers` | one full lifecycle over the public HTTP surface: registration, team creation, invitation join, eligibility questionnaire, theme selection, product submission, bumps, voting, finished results with nomination authorship |
| `TestPublicDisclosureMatrixAcrossVisibilityAndStages` | visibility and stage disclosure across all five stages plus the hidden draft |
| `TestVotingHTTPMutationCountsSelfVoteAndConcurrency` | vote mutation, self-vote ban, live counts, concurrent single-selection uniqueness |
| `TestFinishedNominationResultsSingleTieAndZero` | single winner, ties (multiple winners, no tie-break), zero-vote nomination |
| `TestQuestionnaireResetAndAdminReports` | destructive reset immutability, reports and formula-safe CSV |
| `TestCheckUpToDateReportsMigratedAndStaleSchemas` | migration integrity |

## Manual acceptance

Prerequisites: production-like deployment per `docs/production.md` (or a
development instance), one admin account created with the CLI, a browser, and
a second account for the "outsider" checks.

### 1. Instance and admin

- [ ] `systemctl status jamcontests` is `active`; `GET /health` returns 200 with
      database status; `/admin` requires the session cookie and returns a login
      flow, never a public link (there is no admin link anywhere in the UI).
- [ ] `jamcontests admin add --username admin --password ...` creates the first
      admin; a second run rejects an existing account.

### 2. Jam setup (admin)

- [ ] Create a draft jam with a schedule, add themes and questionnaire questions
      (short text, single choice, multiple choice), and confirm that publishing
      is impossible before at least one question and one theme exist.
- [ ] Publish the jam; it appears on the public homepage as `upcoming` with a
      countdown that never authorizes anything (same page refreshed after a
      deadline shows the new stage without cron).

### 3. Teams and questionnaire (upcoming)

- [ ] Register three accounts; a guest never sees teams, themes, or products
      of the upcoming jam.
- [ ] Captain creates a team with avatar, description. Second team with the
      same name (case-insensitively) is rejected.
- [ ] Captain issues an invite, a member joins, a second invite from the same
      team replaces the first and the old link stops working immediately.
- [ ] Captain and member complete the questionnaire; autosave survives a page
      reload; completion is rejected while any required answer is missing.
- [ ] Members see each other's completion status but not answers; the captain
      sees no answers either.
- [ ] A user from another jam's team is refused by the invite with a clear
      message and keeps their old membership.

### 4. Submission

- [ ] Themes become visible on the jam page automatically at the deadline.
- [ ] Captain chooses a theme, then changes it once; a member's attempt to
      change it is rejected.
- [ ] Captain saves the product card (title, result URL, description, optional
      commentary URL and notes), proposes an optional nomination, and finalizes
      the submission. Without a theme, finalization is blocked with an
      explanatory error.
- [ ] Guests do not see the product, the theme pick, or other teams' selections
      during submission (direct URL requests included).
- [ ] A member of the team cannot finalize or edit when the stage moved to
      evaluation (deadline passes).

### 5. Evaluation

- [ ] Submitted products and their themes become visible to guests.
- [ ] Any registered user can bump a product (own team's product included);
      an immediate second bump is rejected with a cooldown countdown; the
      counter updates for all viewers in real time.
- [ ] The questionnaire editor and theme selection are closed for users.

### 6. Voting

- [ ] Nomination titles and curator marks appear; team-nomination authorship is
      hidden in the UI and in the page source.
- [ ] A user selects a product per nomination, changes the selection, and
      cannot vote for their own team's product; the total per nomination updates
      in real time for everyone.
- [ ] Two products with the same maximum count are both displayed as winners
      with no tie-break, in the finished view.

### 7. Finished and archive

- [ ] Voting and bumps are closed with clear errors; finished results show the
      nomination authors and the winners; the jam stays in `/archive` with its
      public bump counter and product cards.
- [ ] Editing historical data (products, answers, votes) is not possible from
      the UI; admin interventions require a reason and appear in `/admin/audit`
      with before/after material state.
- [ ] A draft jam never escapes to any surface (homepage, archive, search
      results, counters, APIs, error messages).

### 8. Backup and restore

- [ ] `scripts/backup.sh` succeeds, the dump verifier reports a restorable
      backup, and a fresh restore into a scratch database lets the application
      start and serve the archive page. See `docs/backup-restore.md`.

## Failure policy

If any manual step fails, record the fixture (step, expected vs observed,
screenshot if applicable) and do not release. Automated tests must be extended
for any reproducible regression before the next run.