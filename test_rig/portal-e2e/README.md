# Portal E2E Fixture

This directory is a local manual-debug fixture for exercising `stave portal`
commands against a throwaway project.

The fixture uses an isolated `HOME` under `test_rig/tmp/portal-e2e/home` so it
does not mutate the operator's real Stave config. Command evidence goes under
`test_rig/tmp/logs/portal-e2e/`.
