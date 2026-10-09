# Writing documentation

Write for the reader's next task. Before adding a paragraph, decide which
question it answers and which page should own the answer.

## Choose a page

| Reader needs | Put it in |
| --- | --- |
| A first successful connection | Getting started |
| Steps to complete a task | Recipes, assessment workflows, or a focused UI guide |
| Flags, fields, defaults, output states, or compatibility limits | CLI, configuration, or the relevant topic reference |
| Why components behave as they do | Architecture |
| How to develop or verify a change | Testbed, testing, or test catalogue |

Keep architecture focused on current components, data flow, and invariants.
Describe schema compatibility where readers interpret older captures; avoid
a running history of schema additions. Testing owns execution and acceptance
limits; the catalogue owns suite and fixture matrices.

## Edit for clarity

Lead with the action or fact. Give each paragraph one subject. Use concrete
words and active verbs, and cut introductions or summaries that repeat the
heading or preceding paragraph.

Keep one full explanation of a behavior and link to it from other pages.
Repeat a short warning when it changes what the reader should do, especially
for credentials, partial coverage, destructive local maintenance, or sensitive
exports. Keep examples near the instructions they illustrate.

Use tables for flags, keys, statuses, and comparisons. Use a diagram when
relationships are easier to see than to describe. Headings use sentence case;
bold text and callouts should help readers locate a decision or warning.

Delete implementation narration that does not help the page's reader.
Keep boundaries that affect interpretation: offline versus live reads,
missing versus empty evidence, and synthetic versus real-vSphere validation.
Check disputed facts against code and tests before rewriting them.

## Verify a change

Preserve command syntax, defaults, units, exit codes, examples, privacy
requirements, and supported-version caveats. Update links when moving a
section; retain an explicit HTML anchor on the old page for existing bookmarks.

Build with the pinned documentation dependencies:

```sh
python -m pip install --requirement requirements-docs.txt
mkdocs build --strict
```

Check the generated navigation and the links to moved sections. For release
image examples, preserve the `x-release-please` markers and run
`scripts/check-release-pins.sh`.
