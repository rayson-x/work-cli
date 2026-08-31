# Demo Coverage Template

> This file is a demo template only.
> It shows the expected `coverage.md` shape for real domains under `tests/cli_e2e/{domain}`.
> The numbers, command list, and coverage status below are illustrative, not authoritative.
> `tests/cli_e2e/demo/` is reference material and is not part of formal CLI E2E coverage accounting.
> `work-cli demo --help` does not exist, so this file cannot be recomputed from live domain help output.

## Metrics

- Denominator: 8 leaf commands
- Covered: 3
- Coverage: 37.5%

## Summary

- Purpose: show humans and AI agents how to maintain a per-domain coverage file even when the directory is documentation-only and not backed by a real `work-cli demo` command tree.
- TestDemo_TaskLifecycle: demonstrates one minimal task lifecycle workflow for documentation purposes.
- TestDemo_TaskLifecycle/create: runs `task +create` with `summary` and `description`, captures the returned `taskGUID`, and registers parent cleanup for later teardown.
- TestDemo_TaskLifecycle/update: runs `task +update --task-id <guid>` and mutates both `summary` and `description` on the created task.
- TestDemo_TaskLifecycle/get: runs `task tasks get` for the same task and asserts the persisted `guid`, updated `summary`, and updated `description`.
- Cleanup note: `task tasks delete` is executed in `parentT.Cleanup`, but this template intentionally keeps cleanup-only execution marked uncovered so workflow assertions remain distinct from teardown mechanics.
- Demo-only gap note: `task +complete`, `task +reopen`, `task +assign`, and `task +get-my-tasks` are intentionally left as uncovered examples for a minimal template.

## Command Table

| Status | Cmd | Type | Testcase | Key parameter shapes | Notes / uncovered reason |
| --- | --- | --- | --- | --- | --- |
| ✓ | task +create | shortcut | task_lifecycle_test.go::TestDemo_TaskLifecycle/create | basic create; summary; description | demo example |
| ✓ | task +update | shortcut | task_lifecycle_test.go::TestDemo_TaskLifecycle/update | --task-id; update summary; update description | demo example |
| ✓ | task tasks get | api | task_lifecycle_test.go::TestDemo_TaskLifecycle/get | task_guid in --params | demo example |
| ✕ | task tasks delete | api |  | none | cleanup exists in parentT.Cleanup, but demo coverage intentionally treats cleanup-only execution as uncovered |
| ✕ | task +complete | shortcut |  | none | not shown in this minimal lifecycle example |
| ✕ | task +reopen | shortcut |  | none | not shown in this minimal lifecycle example |
| ✕ | task +assign | shortcut |  | none | example of a user-identity-sensitive command; requires real user fixtures |
| ✕ | task +get-my-tasks | shortcut |  | none | example of a current-user-dependent command; often unavailable in bot-only environments |

## Notes

- In a real domain, recompute the denominator from live `work-cli --help` exploration instead of copying this file.
- Replace demo rows with real command inventory for that domain.
- Keep skipped commands unchecked; reuse the `t.Skip(...)` reason as the uncovered reason.
