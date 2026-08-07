---
title: "Reference"
weight: 20
---

# Reference

The command reference for `goblind` and `goblinctl`.

Nothing here is hand-written. Every page is produced by
`mage docs:generate` from the cobra command trees the binaries actually
run, and held to them by `mage docs:check`, which runs inside
`mage lint` on every pull request.

- **[goblinctl](cli/goblinctl/goblinctl/)** - the control client. Its
  `agent` namespace is the embedded kernel's own verbs, mounted here, so
  these pages cover both goblin's surface and the supervisor's.
- **[goblind](cli/goblind/goblind/)** - the orchestrator daemon.

The drift gate regenerates into a temporary tree and byte-compares
rather than regenerating in place, because a gate that repairs the drift
it is measuring passes on its second run and the defect is gone before
anyone reads the message. It reports three conditions: *stale* means the
source moved and the page did not, *missing* means a page under drift
control is no longer produced, and *untracked* means generation produces
a file nothing gates - which is the state this whole section was in
until GOBLIN-DIV-078 closed.
