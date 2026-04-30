# Issues — nightly-no-text-fix

## 2026-04-30 Task: session-start
- No dedicated Python regression tests exist for `/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py`.

## 2026-04-30 Task: backup-baseline
- The current `tg_listener.log` snapshot does not expose a separate raw secret leak, but the fallback-specific evidence is only the embedded recovery/fallback reporter section; keep later edits from dumping full payloads into logs.
- No operational source files were changed during evidence capture.

## 2026-04-30 Task: backup-baseline-correction
- Corrected evidence file to include the actual Telegram no-text fallback send lines from `tg_listener.log` (01:02 and 03:02).

## 2026-04-30 Task: daemon-first-switch
- No new issues surfaced during the adapter swap or compile check.

## 2026-04-30 Task: rollback-handoff
- `test_night_cron_runner_daemon_first.py` was created fresh, so rollback is deletion rather than restore from backup.

## 2026-04-30 Task: final-verification-wave-fixes
- `night_cron_runner.py` needed a non-runtime `pyright` directive because its runtime `sys.path` import pattern leaves `tg_listener` unresolved to static analysis.
- `tg_listener.py` still has pre-existing unrelated diagnostics, but it remains out of scope because this wave did not edit it and its SHA stayed aligned with the backup baseline.
