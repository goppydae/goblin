# Ecosystem CVRL Migration

Roll out the stabilized Canonical Verification Runtime Layout (CVRL) from `goppydae-silo` to `gapi` and `goblin` to ensure consistent linting, path hygiene, and CI reliability across all projects.

## Proposed Changes

### [GAPI Migration]

Standardize `gapi` to match the stabilized `silo` configuration.

#### [MODIFY] [tools/](tools/)

- Sync all scripts and linters from `goppydae-silo/tools/`.
- Standardize all paths to `artifacts/`.

#### [MODIFY] [AGENTS.md](AGENTS.md)

- Update with stabilized CVRL definitions.

#### [NEW] [agenda_state.json](artifacts/history/agenda_state.json)

- Initialize/Standardize the agenda state to satisfy `history_lint`.

______________________________________________________________________

### [GOBLIN Migration]

Standardize `goblin` to match the stabilized `silo` configuration.

#### [MODIFY] [tools/](tools/)

- Sync all scripts and linters from `goppydae-silo/tools/`.
- Standardize all paths to `artifacts/`.

#### [MODIFY] [AGENTS.md](AGENTS.md)

- Update with stabilized CVRL definitions.

#### [NEW] [agenda_state.json](artifacts/history/agenda_state.json)

- Initialize/Standardize the agenda state to satisfy `history_lint`.

______________________________________________________________________

## Verification Plan

### Automated Tests

- Run `tools/verify_all.sh` in `gapi`.
- Run `tools/verify_all.sh` in `goblin`.
- Ensure 100% pass rate in both projects.

### Manual Verification

- Verify that `artifacts/history/history.ndjson` is correctly aggregated in each project.
- Confirm artifacts for this migration are correctly stored in each project's own `artifacts/history/runs/` directory.
