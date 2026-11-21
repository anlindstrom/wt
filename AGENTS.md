# wt

Before changing code you have not worked in, read `docs/architecture.md`: the package layers, the main flows by function, the on-disk state and the safety invariants a change keeps.

## What's new

Every user-visible change adds one entry to the top of `internal/about/whats-new.md`, newest first, headed `## YYYY-MM-DD HH:MM — <what changed>` in local time: a few lines a person would want read out. On a rebase conflict, keep both entries, newest first. `wt about` shows the newest 5.

## JSON output

`--json` output is a contract with other programs. A change to it updates, in the same commit, the schema in `schema/`, the hand-written prose of `docs/json.md` (its field tables come from the schemas: `make json-docs`), and the schema's `x-version`: minor for an added field or enum value; a wording change needs no bump; anything that breaks a reader — a rename, a removal, a changed type or meaning — is a new major, `*.v2.json`, never an edit to v1.

## Tests

While working, run the package you changed, narrowed with `-run` to the family you touched. Before handing work back, run the check once on the Mac mini, `offload run check`, and wait on it in the background; never run the full suite in a loop. `make test-unit` takes seconds; `make test-fast` skips the slowest tests but still takes minutes; the command packages run side by side. What to run for which change: `docs/testing.md`.
