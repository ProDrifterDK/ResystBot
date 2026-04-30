# Task 1 — Backup + baseline evidence

## Scope
- No operational source files were edited.
- Backups were captured before any future edit.

## Backups

| Original | Backup | Bytes | Mode | Owner:Group | SHA-256 |
|---|---|---:|---:|---|---|
| `/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py` | `/home/prodrifterdk/.picoclaw/workspace/backups/nightly-no-text-fix-20260430T111449/night_cron_runner.py.bak` | 1963 | 775 | `prodrifterdk:prodrifterdk` | `e92ac41a6bb17401bcbb8edb1e74d999d908248ec9a77a1376da05eb4092f19e` |
| `/home/prodrifterdk/.picoclaw/workspace/tg_listener.py` | `/home/prodrifterdk/.picoclaw/workspace/backups/nightly-no-text-fix-20260430T111449/tg_listener.py.bak` | 236073 | 644 | `prodrifterdk:prodrifterdk` | `a21a62b5c2f2d00cd0ae57092c7290c454d300223d6c369953c1aa2fb281e832` |

## Rollback commands
```bash
cp -a "/home/prodrifterdk/.picoclaw/workspace/backups/nightly-no-text-fix-20260430T111449/night_cron_runner.py.bak" "/home/prodrifterdk/.picoclaw/workspace/cron/night_cron_runner.py"
cp -a "/home/prodrifterdk/.picoclaw/workspace/backups/nightly-no-text-fix-20260430T111449/tg_listener.py.bak" "/home/prodrifterdk/.picoclaw/workspace/tg_listener.py"
```

## Sanitized baseline excerpts

### TTFT failure + no-output baseline
```text
2026-04-30 01:01:02,065 - DEBUG - [Thread-1 (watchdog)] - [MsgHandler-221899910] Watchdog: process alive (TTFT-wait, elapsed=60s)
2026-04-30 01:02:02,066 - ERROR - [Thread-1 (watchdog)] - [MsgHandler-221899910] Watchdog: TTFT exceeded — no output after 120s, killing process
2026-04-30 01:02:02,070 - WARNING - [MainThread] - [MsgHandler-221899910] No output produced by picoclaw process
2026-04-30 01:02:02,070 - INFO - [MainThread] - [MsgHandler-221899910] Process completed after 120s
```

### Repeated baseline confirmation
```text
2026-04-30 03:01:01,249 - DEBUG - [Thread-1 (watchdog)] - [MsgHandler-221899910] Watchdog: process alive (TTFT-wait, elapsed=60s)
2026-04-30 03:02:01,250 - ERROR - [Thread-1 (watchdog)] - [MsgHandler-221899910] Watchdog: TTFT exceeded — no output after 120s, killing process
2026-04-30 03:02:01,254 - WARNING - [MainThread] - [MsgHandler-221899910] No output produced by picoclaw process
```

### Telegram no-text fallback send lines
```text
2026-04-30 01:02:02,071 - INFO - [MainThread] - Sending message to chat 221899910: ⚠️ Sesión nocturna completada sin respuesta de tex...
2026-04-30 03:02:01,254 - INFO - [MainThread] - Sending message to chat 221899910: ⚠️ Sesión nocturna completada sin respuesta de tex...
```

## Notes
- Current log snapshot exposes the TTFT/no-output failure clearly.
- The Telegram no-text fallback send lines are now captured directly from the log snapshot; no raw secret values or full payloads were copied.
