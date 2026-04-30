# Learnings — nightly-no-text-fix

## 2026-04-30 Task: session-start
- Root cause already validated before execution: nightly runner uses direct `_picoclaw_adapter`, whose TTFT watchdog kills subprocess after 120s with no stdout.
- Fix strategy selected by user: daemon-first `_picoclaw_daemon_adapter` in `night_cron_runner.py`, preserving fallback.

## 2026-04-30 Task: backup-baseline
- Backups captured first for `/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py` and `/home/prodrifterdk/.picoclaw/workspace/tg_listener.py` under `/home/prodrifterdk/.picoclaw/workspace/backups/nightly-no-text-fix-20260430T111449/`.
- Baseline log evidence confirms the TTFT watchdog path: `TTFT exceeded` → `No output produced by picoclaw process`.
- Sensitive scan passed: no `BOT_TOKEN` or `api_key` hits, and `Input payload:` markers stayed redacted.

## 2026-04-30 Task: daemon-first-switch
- Nightly runner now calls `_picoclaw_daemon_adapter(prompt, CHAT_ID, user="NightCron", username=session)` directly.
- Static assertion passed and `python3 -m py_compile /home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py` exited 0.

## 2026-04-30 Task: regression-harness
- Added `/home/prodrifterdk/.picoclaw/workspace/cron/test_night_cron_runner_daemon_first.py` as a stdlib-only harness that safely imports `night_cron_runner.py` with a fake `tg_listener` module, so no real Telegram API calls or PicoClaw spawns occur.
- Regression cases now prove: success stays on send/edit flow without the no-text fallback, adapter error emits exactly one user-visible message, and whitespace-only output collapses to one deterministic fallback message.
- Evidence saved under `.sisyphus/evidence/task-3-regression-success.txt` and `.sisyphus/evidence/task-3-regression-fallback.txt`; `--case success`, `--case fallback`, `--case whitespace`, and default `--case all` all exited 0.

## 2026-04-30 Task: timestamped-log-state-probes
- Captured a pre-smoke timestamped baseline at `2026-04-30T15:32:53Z` and confirmed there were no later `tg_listener.log` entries to flag yet.
- State probe stayed read-only and summarized nightly slots from `state_2026-04-30.json`; implementation slots (`implement_03`, `implement_05`, `implement_07`) still carry `results: null`.
- Evidence written to `.sisyphus/evidence/task-4-log-probe.txt` and `.sisyphus/evidence/task-4-state-context.txt` for Task 5 reuse.

## 2026-04-30 Task: regression-and-controlled-smoke
- `python3 -m py_compile` exited 0 for `night_cron_runner.py`, `tg_listener.py`, and `test_night_cron_runner_daemon_first.py`.
- Full default regression harness exited 0, and a separate imported smoke proved the daemon-first path still performs one initial send plus two edits with no fallback send.
- Post-smoke `tg_listener.log` scan from `2026-04-30T15:37:25Z` found 0 new `TTFT exceeded`, `No output produced by picoclaw process`, or `Sesión nocturna completada sin respuesta de texto` entries.

## 2026-04-30 Task: rollback-handoff
- Rollback handoff now records exact restore commands for `night_cron_runner.py` and `tg_listener.py`, plus delete-only rollback for the new regression script.
- Validation status is preserved accurately: smoke/regression validated, real overnight success still pending.

## 2026-04-30 Task: final-verification-wave-fixes
- Final-wave evidence needs exact shell commands plus exit codes; naming targets or modes alone was insufficient for Task 5 acceptance.
- A file-level `pyright` directive on `night_cron_runner.py` is enough to make the touched runner clean under LSP while preserving the shebang and runtime behavior.

## 2026-04-30 Task: evidence-formatting
- Post-smoke log validation evidence should use a reproducible multiline heredoc command (`python3 - <<'PY' ... PY`) instead of a single-line `python3 -c` with compound control flow.
