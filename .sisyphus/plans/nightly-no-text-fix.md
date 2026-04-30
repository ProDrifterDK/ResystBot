# Nightly No-Text Sessions Fix

## TL;DR
> **Summary**: Nightly sessions are being killed by the direct subprocess adapter's 120s TTFT watchdog before PicoClaw emits stdout. Fix by routing the nightly runner through the existing daemon adapter, while preserving direct fallback behavior and adding focused Python regression/QA.
> **Deliverables**:
> - Operational backup + rollback artifacts for the affected Python scripts.
> - `night_cron_runner.py` switched to daemon-first `_picoclaw_daemon_adapter`.
> - Stdlib Python regression script covering daemon success, daemon fallback/error, and no duplicate sends.
> - Smoke/log validation proving no TTFT/no-text failure for controlled runs.
> **Effort**: Short
> **Parallel**: YES - 2 waves
> **Critical Path**: Task 1 → Task 2 → Task 3 → Task 5 → Final Verification

## Context
### Original Request
Alan asked to verify why all nightly sessions only returned `⚠️ Sesión nocturna completada sin respuesta de texto.`, suspected z.ai provider failure, and requested log review under `/home/prodrifterdk/.picoclaw/workspace/logs/` plus fix proposals.

### Interview Summary
- Diagnosis confirmed: this is not primarily a z.ai outage.
- User selected **Daemon-first + fallback** as the fix strategy.
- User authorized editing operational scripts under `~/.picoclaw/workspace` with backup and rollback.
- User selected **Regression script + automated QA** for verification.

### Metis Review (gaps addressed)
- Guardrails: backup before edits, preserve permissions/shebang/import behavior, avoid provider config changes, avoid global TTFT increase as primary fix, preserve fallback, prevent duplicate Telegram sends, avoid logging secrets/full prompts.
- Edge cases: daemon unavailable, daemon dies after partial content, whitespace-only content, import failure under cron env, concurrent nightly slots.
- Acceptance criteria added for py_compile, static adapter assertion, mocked daemon success/fallback, and log QA.

## Work Objectives
### Core Objective
Make nightly cron sessions use PicoClaw daemon transport first so long tool-call phases no longer get killed by `_picoclaw_adapter`'s 120s stdout TTFT watchdog.

### Deliverables
- Backup files with checksums under `/home/prodrifterdk/.picoclaw/workspace/backups/` or timestamped sibling `.bak.*` files.
- Updated `/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py`.
- New regression script at `/home/prodrifterdk/.picoclaw/workspace/cron/test_night_cron_runner_daemon_first.py` using Python stdlib only.
- Evidence files under `.sisyphus/evidence/`.

### Definition of Done (verifiable conditions with commands)
- `python3 -m py_compile /home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py /home/prodrifterdk/.picoclaw/workspace/tg_listener.py /home/prodrifterdk/.picoclaw/workspace/cron/test_night_cron_runner_daemon_first.py` exits `0`.
- Static assertion confirms `night_cron_runner.py` imports and calls `_picoclaw_daemon_adapter`, not `_picoclaw_adapter` as the primary nightly path.
- Regression script exits `0` and writes evidence proving: daemon success emits user text; fallback/error emits exactly once; no duplicate Telegram sends.
- Controlled smoke run produces no new `TTFT exceeded`, `No output produced by picoclaw process`, or `Sesión nocturna completada sin respuesta de texto` entries for the smoke timestamp.

### Must Have
- Surgical edit: primary code change in `night_cron_runner.py` only unless a test proves `tg_listener.py` needs a compatibility fix.
- Preserve `#!/usr/bin/env python3`, file permissions, ownership, `sys.path.insert(0, '/home/prodrifterdk/.picoclaw/workspace')`, and existing Telegram send/edit behavior.
- Use `_picoclaw_daemon_adapter(prompt, CHAT_ID, user="NightCron", username=session)` as the primary adapter call.
- Keep the existing no-text fallback text unchanged: `⚠️ Sesión nocturna completada sin respuesta de texto.`
- Backups and rollback command recorded before edits.

### Must NOT Have (guardrails, AI slop patterns, scope boundaries)
- Do NOT change z.ai/provider/model configuration.
- Do NOT globally increase `TTFT_TIMEOUT` as the primary fix.
- Do NOT rewrite `tg_listener.py`, cron orchestration, or daemon manager broadly.
- Do NOT log bot token, full prompt payloads, or unnecessary chat-sensitive data in evidence.
- Do NOT touch Go repo code unless explicitly needed for evidence collection.

## Verification Strategy
> ZERO HUMAN INTERVENTION - all verification is agent-executed.
- Test decision: Regression script + automated QA, using Python stdlib because no Python test framework exists.
- QA policy: Every task has agent-executed scenarios.
- Evidence: `.sisyphus/evidence/task-{N}-{slug}.{ext}`

## Execution Strategy
### Parallel Execution Waves
> Target: 5-8 tasks per wave. <3 per wave (except final) = under-splitting.
> Extract shared dependencies as Wave-1 tasks for max parallelism.

Wave 1: Tasks 1, 2, 3, 4; Task 2 depends on Task 1 backup, and Task 3 starts after Task 2 defines the final runner API.
Wave 2: Tasks 5 and 6 after Task 2 + Task 3.

### Dependency Matrix (full, all tasks)
| Task | Blocks | Blocked By |
|---|---|---|
| 1 Backup + baseline evidence | 2, 5, 6 | none |
| 2 Switch runner to daemon-first | 3, 5, 6 | 1 |
| 3 Add regression script | 5, 6 | 1, 2 |
| 4 Prepare log/state QA probes | 5, 6 | none |
| 5 Run regression + smoke validation | 6 | 2, 3, 4 |
| 6 Rollback docs + operational handoff | Final Verification | 5 |

### Agent Dispatch Summary (wave → task count → categories)
- Wave 1 → 4 tasks → quick, unspecified-high
- Wave 2 → 2 tasks → unspecified-high
- Final Verification → 4 review tasks → oracle, unspecified-high, deep

## TODOs
> Implementation + Test = ONE task. Never separate.
> EVERY task MUST have: Agent Profile + Parallelization + QA Scenarios.

- [x] 1. Capture backups and baseline failure evidence

  **What to do**: Create timestamped backups before any edit for `/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py` and `/home/prodrifterdk/.picoclaw/workspace/tg_listener.py`. Record `sha256sum`, permissions, owner/group, and last 2026-04-30 failure excerpts from `/home/prodrifterdk/.picoclaw/workspace/logs/tg_listener.log` into `.sisyphus/evidence/task-1-backup-baseline.md`.
  **Must NOT do**: Do not edit either operational script in this task. Do not copy bot tokens or full prompts into evidence.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: deterministic filesystem evidence and backups.
  - Skills: [] - no specialized skill needed.
  - Omitted: [`git-master`] - no commit should be made for workspace-only operational files.

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: [2, 5, 6] | Blocked By: []

  **References**:
  - Failure log: `/home/prodrifterdk/.picoclaw/workspace/logs/tg_listener.log` - contains `TTFT exceeded`, `No output produced by picoclaw process`, and no-text send sequence.
  - Runner: `/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py:1-57` - actual failing nightly runner.
  - Direct adapter: `/home/prodrifterdk/.picoclaw/workspace/tg_listener.py:2721-2934` - subprocess TTFT watchdog.

  **Acceptance Criteria**:
  - [ ] Backup files exist and are non-empty.
  - [ ] Evidence file contains checksum, permission, owner/group, backup path, and rollback copy command.
  - [ ] Evidence file includes sanitized baseline excerpts proving the TTFT/no-text chain.

  **QA Scenarios**:
  ```
  Scenario: Backup integrity
    Tool: Bash
    Steps: Run sha256sum/stat on original and backup files; compare byte sizes.
    Expected: backup size equals original size; checksum recorded; permissions recorded.
    Evidence: .sisyphus/evidence/task-1-backup-baseline.md

  Scenario: Sensitive data guard
    Tool: Bash
    Steps: Scan evidence for `BOT_TOKEN`, `api_key`, and full `Input payload:` blocks.
    Expected: no secrets/full prompts present.
    Evidence: .sisyphus/evidence/task-1-backup-baseline-sensitive-scan.txt
  ```

  **Commit**: NO | Message: n/a | Files: external operational backups/evidence only

- [x] 2. Switch nightly runner to daemon-first adapter

  **What to do**: Edit `/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py` surgically: line 7 should import `_picoclaw_daemon_adapter` instead of `_picoclaw_adapter`; line 21 should iterate `_picoclaw_daemon_adapter(prompt, CHAT_ID, user="NightCron", username=session)`. Preserve send/edit loop behavior lines 22-48 and the no-text fallback string exactly.
  **Must NOT do**: Do not edit `TTFT_TIMEOUT`; do not modify provider/model config; do not remove no-text fallback; do not alter Telegram message formatting.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: two-line surgical Python change.
  - Skills: [] - no specialized skill needed.
  - Omitted: [`gitnexus-refactoring`] - target file is outside indexed repo and no symbol rename is required.

  **Parallelization**: Can Parallel: NO | Wave 1 | Blocks: [3, 5, 6] | Blocked By: [1]

  **References**:
  - Current import/call/fallback: `/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py:7`, `:21`, `:47-48`.
  - Daemon adapter signature: `/home/prodrifterdk/.picoclaw/workspace/tg_listener.py:3531-3571`.
  - Daemon fallback behavior: `/home/prodrifterdk/.picoclaw/workspace/tg_listener.py:3585-3597`.

  **Acceptance Criteria**:
  - [ ] Static check: file contains `_picoclaw_daemon_adapter(prompt, CHAT_ID, user="NightCron", username=session)`.
  - [ ] Static check: direct `_picoclaw_adapter(prompt, CHAT_ID)` is not used in `night_cron_runner.py`.
  - [ ] `python3 -m py_compile /home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py` exits `0`.

  **QA Scenarios**:
  ```
  Scenario: Primary path is daemon adapter
    Tool: Bash
    Steps: Run a Python AST/static assertion over night_cron_runner.py.
    Expected: import and call target `_picoclaw_daemon_adapter`; no primary direct `_picoclaw_adapter` call remains.
    Evidence: .sisyphus/evidence/task-2-daemon-first-static.txt

  Scenario: Syntax/import sanity
    Tool: Bash
    Steps: Run `python3 -m py_compile` for night_cron_runner.py.
    Expected: exit 0, no syntax/import-time errors.
    Evidence: .sisyphus/evidence/task-2-pycompile.txt
  ```

  **Commit**: NO | Message: n/a | Files: `/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py`

- [x] 3. Add focused Python regression script

  **What to do**: Create `/home/prodrifterdk/.picoclaw/workspace/cron/test_night_cron_runner_daemon_first.py` using Python stdlib `unittest` or plain assertions. Import `night_cron_runner.py`, monkeypatch `_picoclaw_daemon_adapter`, `send_message_get_id`, `edit_message`, and `send_message`. Implement CLI cases `--case success`, `--case fallback`, `--case whitespace`, and default `--case all`. Cover: daemon success with delayed simulated event; daemon error/fallback event; whitespace/no-content path; no duplicate sends.
  **Must NOT do**: Do not require pytest or new packages. Do not call real Telegram API. Do not spawn real PicoClaw.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - Reason: needs careful monkeypatching of operational script without side effects.
  - Skills: [] - Python stdlib only.
  - Omitted: [`context7`] - no external docs required.

  **Parallelization**: Can Parallel: NO | Wave 1 | Blocks: [5, 6] | Blocked By: [1, 2]

  **References**:
  - Runner loop: `/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py:12-48`.
  - Daemon adapter event contract: `/home/prodrifterdk/.picoclaw/workspace/tg_listener.py:3531-3584` returns `(event, content)` tuples.
  - Existing no-test gap: no dedicated `*telegram*_test.go`; Python runner has no test file.

  **Acceptance Criteria**:
  - [ ] Regression script exits `0`.
  - [ ] Test proves daemon success sends/edits final text and does not send no-text fallback.
  - [ ] Test proves error/fallback emits exactly one user-visible message.
  - [ ] Test proves whitespace-only content does not create duplicate Telegram sends.

  **QA Scenarios**:
  ```
  Scenario: Daemon success regression
    Tool: Bash
    Steps: Run `python3 /home/prodrifterdk/.picoclaw/workspace/cron/test_night_cron_runner_daemon_first.py --case success`.
    Expected: exit 0; captured events show final text propagated; no no-text fallback.
    Evidence: .sisyphus/evidence/task-3-regression-success.txt

  Scenario: Daemon fallback/error regression
    Tool: Bash
    Steps: Run `python3 /home/prodrifterdk/.picoclaw/workspace/cron/test_night_cron_runner_daemon_first.py --case fallback`.
    Expected: exit 0; exactly one user-visible fallback/error message; no duplicate send/edit loop.
    Evidence: .sisyphus/evidence/task-3-regression-fallback.txt
  ```

  **Commit**: NO | Message: n/a | Files: `/home/prodrifterdk/.picoclaw/workspace/cron/test_night_cron_runner_daemon_first.py`

- [x] 4. Prepare timestamped log/state QA probes

  **What to do**: Write reusable QA commands/scripts that capture a pre-smoke timestamp and scan only later log lines in `/home/prodrifterdk/.picoclaw/workspace/logs/tg_listener.log` for forbidden patterns: `TTFT exceeded`, `No output produced by picoclaw process`, `Sesión nocturna completada sin respuesta de texto`. Also inspect `/home/prodrifterdk/.picoclaw/workspace/state/nightly/state_2026-04-30.json` or current-date state for `results: null` only as contextual symptom, not as the sole pass/fail criterion.
  **Must NOT do**: Do not alter state files. Do not require waiting for real overnight schedule.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: deterministic log scanner/evidence collection.
  - Skills: [] - no specialized skill needed.
  - Omitted: [`gitnexus-debugging`] - root cause already validated.

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: [5, 6] | Blocked By: []

  **References**:
  - Current log: `/home/prodrifterdk/.picoclaw/workspace/logs/tg_listener.log`.
  - Historical log: `/home/prodrifterdk/.picoclaw/workspace/logs/tg_listener.log.2026-04-29`.
  - State symptom: `/home/prodrifterdk/.picoclaw/workspace/state/nightly/state_2026-04-30.json`.

  **Acceptance Criteria**:
  - [ ] QA probe records pre-smoke timestamp.
  - [ ] QA probe scans only entries after pre-smoke timestamp.
  - [ ] Probe output is saved under `.sisyphus/evidence/`.

  **QA Scenarios**:
  ```
  Scenario: Forbidden-pattern scanner
    Tool: Bash
    Steps: Run scanner against current tg_listener.log from captured timestamp.
    Expected: reports zero forbidden patterns for new smoke run window.
    Evidence: .sisyphus/evidence/task-4-log-probe.txt

  Scenario: State context scanner
    Tool: Bash
    Steps: Read current-date nightly state and summarize status/result fields without editing.
    Expected: output captured; no writes to state file.
    Evidence: .sisyphus/evidence/task-4-state-context.txt
  ```

  **Commit**: NO | Message: n/a | Files: evidence only

- [x] 5. Run regression and controlled smoke validation

  **What to do**: Run py_compile, the full regression script, and a controlled non-network smoke invocation of `run_night_cron` with monkeypatched Telegram send/edit functions and a fake daemon adapter event stream. Save logs and pass/fail output.
  **Must NOT do**: Do not wait for the real overnight schedule. Do not send real Telegram messages in this task. Do not run a prompt that can trigger long autonomous code changes.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - Reason: validates operational behavior while avoiding real-message side effects.
  - Skills: [] - no specialized skill needed.
  - Omitted: [`dev-browser`] - no browser/UI involved.

  **Parallelization**: Can Parallel: NO | Wave 2 | Blocks: [6] | Blocked By: [2, 3, 4]

  **References**:
  - Compile targets: `/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py`, `/home/prodrifterdk/.picoclaw/workspace/tg_listener.py`, regression script.
  - Log forbidden patterns from baseline: `TTFT exceeded`, `No output produced by picoclaw process`, `Sesión nocturna completada sin respuesta de texto`.
  - Daemon adapter success path: `/home/prodrifterdk/.picoclaw/workspace/tg_listener.py:3563-3584`.

  **Acceptance Criteria**:
  - [ ] `py_compile` exits `0` for all Python files touched/added.
  - [ ] Regression script exits `0`.
  - [ ] Smoke/log probe shows no forbidden patterns after smoke timestamp.
  - [ ] Evidence includes exact command lines, exit codes, and sanitized outputs.

  **QA Scenarios**:
  ```
  Scenario: Full automated regression
    Tool: Bash
    Steps: Run compile + regression script.
    Expected: all commands exit 0; output saved.
    Evidence: .sisyphus/evidence/task-5-regression-run.txt

  Scenario: Smoke/log validation
    Tool: Bash
    Steps: Capture timestamp, run controlled smoke, scan later logs for forbidden patterns.
    Expected: no new TTFT/no-output/no-text fallback lines.
    Evidence: .sisyphus/evidence/task-5-smoke-log-validation.txt
  ```

  **Commit**: NO | Message: n/a | Files: external workspace scripts/evidence only

- [x] 6. Document rollback and operational handoff

  **What to do**: Create `.sisyphus/evidence/task-6-rollback-handoff.md` containing backup paths, exact rollback commands, changed file summary, test evidence links, and next real overnight monitoring checklist. Include how to revert if daemon-first smoke fails, duplicate messages appear, or daemon is unavailable.
  **Must NOT do**: Do not claim real overnight success unless an actual overnight slot has already run after the patch.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: documentation/evidence consolidation.
  - Skills: [] - no specialized skill needed.
  - Omitted: [`vault-sync`] - not needed unless user asks to persist lesson to vault.

  **Parallelization**: Can Parallel: NO | Wave 2 | Blocks: [Final Verification] | Blocked By: [5]

  **References**:
  - Backup evidence: `.sisyphus/evidence/task-1-backup-baseline.md`.
  - Regression evidence: `.sisyphus/evidence/task-5-regression-run.txt`.
  - Smoke evidence: `.sisyphus/evidence/task-5-smoke-log-validation.txt`.

  **Acceptance Criteria**:
  - [ ] Handoff doc includes rollback command for every edited file.
  - [ ] Handoff doc includes monitor command/log path for next 01:00/03:00/05:00/07:00 slots.
  - [ ] Handoff doc clearly states whether success is smoke-validated only or real overnight-validated.

  **QA Scenarios**:
  ```
  Scenario: Rollback command completeness
    Tool: Bash
    Steps: Parse handoff doc for every edited path and matching backup path/restore command.
    Expected: every edited file has a backup and rollback command.
    Evidence: .sisyphus/evidence/task-6-rollback-check.txt

  Scenario: No false completion claim
    Tool: Bash
    Steps: Scan handoff doc for claims of real overnight success.
    Expected: only claims backed by evidence timestamps are present.
    Evidence: .sisyphus/evidence/task-6-claim-check.txt
  ```

  **Commit**: NO | Message: n/a | Files: `.sisyphus/evidence/task-6-rollback-handoff.md`

## Final Verification Wave (MANDATORY — after ALL implementation tasks)
> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.
> **Do NOT auto-proceed after verification. Wait for user's explicit approval before marking work complete.**
> **Never mark F1-F4 as checked before getting user's okay.** Rejection or user feedback -> fix -> re-run -> present again -> wait for okay.
- [x] F1. Plan Compliance Audit — oracle
- [x] F2. Code Quality Review — unspecified-high
- [x] F3. Real Manual QA — unspecified-high
- [x] F4. Scope Fidelity Check — deep

## Commit Strategy
- No git commit by default: primary edits are operational files under `/home/prodrifterdk/.picoclaw/workspace`, outside the ResystBot repo.
- If executor creates repo-tracked evidence changes only, do not commit unless Alan explicitly requests it.

## Success Criteria
- Nightly runner no longer uses direct `_picoclaw_adapter` as primary path.
- Controlled long/no-stdout pre-final-response behavior does not trigger TTFT kill.
- No new no-text fallback is emitted during controlled smoke.
- Backups and rollback commands exist before any operational change.
- User receives consolidated verification results and explicitly approves before completion.
