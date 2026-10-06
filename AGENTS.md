# Repository guidance

Use the checked-in testbed interface for synthetic UI work:

```sh
scripts/testbed list
scripts/testbed test
scripts/testbed pty
scripts/testbed sandbox overview
```

Read [docs/testbed.md](docs/testbed.md) for profile boundaries and
[docs/testing.md](docs/testing.md) for the complete verification ladder. Keep
presentation fixtures deterministic, offline, read-only, and visibly
synthetic. Connected fixtures must remain loopback-only and isolated from
operator configuration, credentials, keyrings, and network state.

When creating an issue, use the repository issue form that fits the work and
set its required Component field. The auto label workflow uses that field; use
`other` when no single area fits. PR labels come from the required
`action(component): summary` title format.

Pull requests are squash-merged, so the PR title becomes the one commit on
`main` and release-please builds the release notes from it. Only `feat`,
`fix`, `perf`, and `revert` appear in the notes. Use `feat` or `fix` only when
shipped behavior changes; use `test` for test-only changes,
`ci(workflows)` for workflow and tooling changes, and `build`, `refactor`,
`docs`, or `chore` otherwise. If one PR mixes a user-facing change with test or
CI work, split it.
