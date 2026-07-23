# Memory e2e (real marmot binary)

Builds marmot from the sibling `context-marmot` checkout (override with
`STAVE_E2E_MARMOT_REPO`), exports `STAVE_E2E_MARMOT`, and runs
`TestMarmotMemoryE2E` in `internal/cli` — stave HEAD against marmot HEAD.
