# Task 6 — Rollback + operational handoff

## Scope
- Documentation plus one post-review, non-runtime `pyright` directive added to `/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py` so touched-file diagnostics are clean without modifying `tg_listener.py`.
- Validation status carried forward from Task 5: smoke/regression validated only.
- Real overnight validation is still pending unless a true overnight slot ran after the patch.

## Changed files
- `/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py`
- `/home/prodrifterdk/.picoclaw/workspace/cron/test_night_cron_runner_daemon_first.py`

## Backups and rollback

### `night_cron_runner.py`
- Backup: `/home/prodrifterdk/.picoclaw/workspace/backups/nightly-no-text-fix-20260430T111449/night_cron_runner.py.bak`
- Restore:
```bash
cp -a "/home/prodrifterdk/.picoclaw/workspace/backups/nightly-no-text-fix-20260430T111449/night_cron_runner.py.bak" "/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py"
```

### `test_night_cron_runner_daemon_first.py`
- File was created as a new regression script.
- No prior backup was found.
- Rollback / removal:
```bash
rm -f "/home/prodrifterdk/.picoclaw/workspace/cron/test_night_cron_runner_daemon_first.py"
```

### `tg_listener.py` (backed up, not edited)
- Backup: `/home/prodrifterdk/.picoclaw/workspace/backups/nightly-no-text-fix-20260430T111449/tg_listener.py.bak`
- Restore:
```bash
cp -a "/home/prodrifterdk/.picoclaw/workspace/backups/nightly-no-text-fix-20260430T111449/tg_listener.py.bak" "/home/prodrifterdk/.picoclaw/workspace/tg_listener.py"
```

## Evidence links
- Task 1: `.sisyphus/evidence/task-1-backup-baseline.md`
- Task 2: `.sisyphus/evidence/task-2-daemon-first-static.txt`, `.sisyphus/evidence/task-2-pycompile.txt`
- Task 3: `.sisyphus/evidence/task-3-regression-success.txt`, `.sisyphus/evidence/task-3-regression-fallback.txt`
- Task 4: `.sisyphus/evidence/task-4-log-probe.txt`, `.sisyphus/evidence/task-4-state-context.txt`
- Task 5: `.sisyphus/evidence/task-5-regression-run.txt`, `.sisyphus/evidence/task-5-smoke-log-validation.txt`

## Validation status
- `py_compile`: passed.
- Regression harness: passed.
- Controlled smoke: passed.
- Post-smoke log scan: zero forbidden hits.
- `lsp_diagnostics`: clean for `night_cron_runner.py` and `test_night_cron_runner_daemon_first.py`.
- `tg_listener.py` diagnostics remain pre-existing and out of scope: the file was not edited in this wave, and its current SHA still matches the backup baseline noted by final verification.
- Real overnight success: **not claimed**.

## Next overnight monitoring checklist
- [ ] 01:00 slot: confirm daemon-first path runs and logs a send/edit flow.
- [ ] 03:00 slot: confirm no `TTFT exceeded` / no-output fallback regressions.
- [ ] 05:00 slot: confirm `tg_listener.log` stays clean after smoke window.
- [ ] 07:00 slot: confirm final consolidation state and capture evidence.
- [ ] Inspect `/home/prodrifterdk/.picoclaw/workspace/logs/tg_listener.log` for forbidden patterns only after each slot.

## Handoff note
- The added `pyright` directive preserves the shebang, changes no runtime behavior, and exists only to keep touched-file diagnostics clean in static review.
- This package is ready for the overnight observer, but it does **not** prove a real overnight run in this session.
