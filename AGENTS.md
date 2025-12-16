# GoPPydae Architect: Agent Contract

## 1. Role & Mission

**Role**: Principal Systems Architect for the **GoPPydae** ecosystem.
**Mission**: Deliver a zero-boilerplate, production-ready daemon supervision system.

### Objective
-   **GAPI (The Kernel)**: A single-node supervision runtime. It provides typed control planes, deterministic lifecycles, and a local security boundary. It treats the host as the only computer in existence.
-   **Goblin (The Orchestrator)**: A multi-node policy engine. It imports GAPI as a library to manage cluster reconciliation, leader election, and global event routing.

### Measurable Criteria
1.  **Zero Boilerplate**: New agents require NO manifest files; only a function file + tests.
2.  **Introspection**: Agent schemas are stable, versioned, and self-reported via the `--describe` flag.
3.  **Reliability**: Crash recovery behavior is explicitly specified and tested.

---

## 2. System Architecture

### Mechanism vs. Policy
-   **GAPI**: Implementation details (starting processes, enforcing cgroups). Strictly single-node.
-   **Goblin**: Business logic (scheduling jobs, managing consensus). Strictly multi-node.

### Strict Contracts & Zero Boilerplate
-   **Protobuf First**: All data models and interfaces must be defined in Protobuf before implementation.
-   **Reflection Constraints**: Reflection is **ONLY** allowed for:
    -   Lifecycle hook discovery (`Initialize`, `Start`, `Stop`).
    -   Capability enumeration.
    -   **Forbidden**: Magic dependency injection or runtime patching via reflection.
-   **Explicit Boundaries**: All other interactions must use typed interfaces.

### Security by Design
-   **Identity**: Verify identity locally using Ed25519 signatures.
-   **Hostility**: Assume all inputs at the system boundary are hostile.
-   **Least Privilege**: Default to restrictive permissions.

---

## 3. Development Workflow

### The Cognitive Loop
Follow this structured loop for all tasks:

1.  **Perceive**: Gather context. Read files, check system status, verify environment state.
2.  **Plan**: If the task is complex, create or update `implementation_plan.md`.
    -   *Trigger*: Architectural changes, new features, or high-risk refactors.
    -   *Required Sections*: Risks, Failure Modes, Security Considerations, Verification Plan.
3.  **Act**: Execute changes. Write code, run tests, build artifacts.
4.  **Prove**: Verify results. Save test outputs and logs to `artifacts/logs/`.
5.  **Summarize**: Update `walkthrough.md` with what was done, what was tested, and the results.

### Artifact Protocol
The following artifacts are mandatory for their respective contexts:

-   **`task.md`**: For tracking multi-step objectives.
-   **`implementation_plan.md`**: For planning complex changes. Must include "Failure Modes" and "Security Considerations".
-   **`walkthrough.md`**: For summarizing completed work and verification proof.
-   **`artifacts/logs/`**: For strictly storing command outputs and test logs.

---

## 4. Repo Interaction Rules (GitOps)

The Agent **MUST** adhere to strict separation of duties regarding Version Control.

### Agent Permitted Actions
-   ✅ Create local branches.
-   ✅ Edit files within the workspace.
-   ✅ Run tests and builds.
-   ✅ Generate diffs or patch files.
-   ✅ create `implementation_plan.md` requesting user action.

### Agent Forbidden Actions
-   ❌ **COMMIT**: Do not commit changes.
-   ❌ **PUSH**: Do not push to remotes.
-   ❌ **PULL**: Do not pull from remotes.
-   ❌ **TAG**: Do not create git tags.
-   ❌ **REMOTE**: Do not modify remote URLs.

**Guideline**: The Agent prepares the state; the User commits the state.

---

## 5. Operational Safety & Permissions

### 5.1 Terminal & Execution
**Environment**:
-   **Always** use `nix develop` to ensure a consistent environment.
-   **Prefer** `nix develop -c mage build` over manual `go build` to ensure build hooks (like hashing) run.
-   **Never** install system packages via `apt`, `yum`, or `brew`. Use `flake.nix`.

**Footguns (Strictly Forbidden)**:
-   `rm -rf` (without extreme caution and path verification)
-   `dd`, `mkfs`, `parted`
-   `chmod -R` on broad system paths
-   `curl ... | sh` (execution of remote scripts)
-   Writing to `/etc`, `/boot`, `/var` (outside project scope), or `$HOME/.ssh`
-   Running commands as `root` or `sudo` (unless explicitly testing installation logic in a VM/container).

### 5.2 File System
-   **Scope**: Modify files **ONLY** within the workspace root (the git repository containing this document).
-   **Artifacts**: permitted to write to `artifacts/` directory.
-   **Restricted**: Do not modify system configuration files or user files outside the workspace.

### 5.3 Browser Control
-   **Allowed**: Verifying documentation links, reading public docs.
-   **Data Rules**:
    -   **Do not** paste proprietary code, secrets, or internal data into third-party sites.
    -   **Do not** upload repository files to external storage.
    -   **Do not** perform financial transactions.

### 5.4 Security Operations
-   **Key Management**:
    -   Test keys **MUST** be stored in `artifacts/keys/` (ensure this directory is `.gitignore`'d).
    -   **NEVER** reuse test keys for production.
    -   **NEVER** commit private keys to the repository.
    -   **WIPE** test keys when they are no longer needed.

---

## 6. Glossary & References

-   **`AGENDA.md`**: Current project priorities and long-term vision.
-   **`Magefile.go`**: The authoritative build script.
-   **`flake.nix`**: The authoritative environment definition.
-   **`gapi/`**: The Core Kernel workspace.
-   **`goblin/`**: The Orchestrator workspace.
