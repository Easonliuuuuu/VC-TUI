const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const path = require("node:path");
const test = require("node:test");

// Run the code embedded in the workflow so tests cover actual GitHub behavior.
const workflow = readFileSync(
  path.join(__dirname, "../.github/workflows/labeler.yml"),
  "utf8"
);
const scriptBlock = workflow.split("          script: |\n")[1];
assert.ok(scriptBlock, "labeler workflow script exists");
const script = scriptBlock
  .split("\n")
  .map((line) => line.replace(/^            /, ""))
  .join("\n");
const runScript = new Function("context", "github", "core", `return (async () => {\n${script}\n})()`);

async function runLabeler({ title, body = "", existingLabels = [], eventName = "issues" }) {
  const applied = [];
  const failed = [];
  const item = {
    number: 17,
    title,
    body,
    labels: existingLabels.map((name) => ({ name }))
  };
  const context = {
    repo: { owner: "example", repo: "vsfleet" },
    eventName,
    payload: eventName === "pull_request_target"
      ? { pull_request: item }
      : { issue: item }
  };
  const github = {
    rest: {
      issues: {
        addLabels: async ({ labels }) => applied.push(...labels),
        getLabel: async () => {},
        createLabel: async () => {}
      }
    }
  };
  await runScript(context, github, {
    info: () => {},
    setFailed: (message) => failed.push(message)
  });
  return { applied, failed };
}

test("issues use only the issue form Component field", async () => {
  assert.deepEqual(await runLabeler({
    title: "Cursor jumps after vCenter refresh",
    body: "The TUI shows findings from RVTools. See config.toml and README."
  }), { applied: [], failed: [] });
  assert.deepEqual(await runLabeler({
    title: "fix(tui): keep selection on refresh",
    body: "This also mentions vSphere, docs, and credentials."
  }), { applied: [], failed: [] });
  assert.deepEqual(await runLabeler({
    title: "Inventory mapping problem",
    body: "### Component\n\nvsphere\n\n### What happened?\n\nA mapping failed."
  }), { applied: ["vsphere"], failed: [] });
  assert.deepEqual(await runLabeler({
    title: "General maintenance",
    body: "### Component\n\nother\n\n### Details\n\nClean up tooling."
  }), { applied: [], failed: [] });
  assert.deepEqual(await runLabeler({
    title: "Misleading body",
    body: "Some prose first.\n\n### Component\n\ntui"
  }), { applied: [], failed: [] });
  assert.deepEqual(await runLabeler({
    title: "Unknown form value",
    body: "### Component\n\nother than tui"
  }), { applied: [], failed: [] });
});

test("PR title component selects exactly one label", async () => {
  for (const [title, expected] of [
    ["fix(vsphere): correct inventory mapping", ["vsphere"]],
    ["docs(docs): clarify setup", ["docs"]],
    ["feat(assessment)!: change findings format", ["assessment"]],
    ["chore(other): tidy shared tooling", []]
  ]) {
    assert.deepEqual(await runLabeler({
      eventName: "pull_request_target",
      title,
      body: "Mentions every other area"
    }), { applied: expected, failed: [] }, title);
  }
});

test("invalid PR titles fail the check and receive no label", async () => {
  for (const title of [
    "Fix inventory mapping",
    "fix: correct inventory mapping",
    "fix(inventory): correct mapping",
    "perf(vsphere): speed up inventory",
    "Fix(vsphere): correct mapping",
    "fix(vsphere):"
  ]) {
    const result = await runLabeler({ eventName: "pull_request_target", title });
    assert.deepEqual(result.applied, [], title);
    assert.match(result.failed[0], /PR title must be action\(component\)/, title);
  }
});

test("existing area labels prevent accumulation but do not bypass title validation", async () => {
  assert.deepEqual(await runLabeler({
    eventName: "pull_request_target",
    title: "fix(vsphere): adjust mapping",
    existingLabels: ["tui"]
  }), { applied: [], failed: [] });
  const invalid = await runLabeler({
    eventName: "pull_request_target",
    title: "Bad title",
    existingLabels: ["tui"]
  });
  assert.deepEqual(invalid.applied, []);
  assert.equal(invalid.failed.length, 1);
});
