---
name: git-commit
description: Stage and commit all changes using conventional commits format, then optionally push and open a PR with a matching template. Use when the user wants to commit current working tree changes with a standardized message, or also wants a PR opened.
license: MIT
metadata:
  author: local
  version: "1.3"
---

Stage and commit all changes using the conventional commits format, then optionally push the branch and open a PR whose description mirrors the commit body.

**Input**: Optional component or hint from the user (e.g., "socks transport", "context wizard"). If omitted, derive everything from the diff.

---

**Authorship**

Commits and PRs are authored by the human running the work. An AI assistant is a tool used to produce the change, not a party to it.

- The commit **author** and **committer** must both be the repository user's own git identity (`user.name` / `user.email`). Never set `GIT_AUTHOR_*` or `GIT_COMMITTER_*`, and never pass `--author`, to an assistant, bot, or `noreply` AI identity.
- Never add a `Co-Authored-By` line naming an AI assistant, in a commit message or anywhere else.
- Never add `Claude-Session`, `Generated with ...`, `🤖`, or any similar assistant attribution trailer or footer to a commit message, a PR title, a PR body, or a PR comment.
- This applies to every commit and PR in this repository, including ones created by an agent on the user's behalf.

---

**Steps**

1. **Inspect the working tree**

   Run in parallel:
   ```bash
   git status
   git diff HEAD
   git diff --staged
   git log --oneline -5
   ```

   Use these to understand:
   - Which files are modified/added/deleted (staged and unstaged)
   - What the actual changes are
   - The recent commit style of this repo

2. **Derive the conventional commit message**

   **Type** — pick exactly one:
   - `feat` — new capability visible to users or API consumers
   - `fix` — corrects a bug or broken behavior
   - `refactor` — restructures code without changing behavior
   - `chore` — dependency updates, config, tooling, cleanup with no behavior change
   - `docs` — documentation only
   - `test` — adds or fixes tests
   - `ci` — CI/CD pipeline changes
   - `build` — build system, Dockerfile, packaging

   **Breaking changes** — mark them explicitly whenever the change breaks an existing public API, CLI flag, config shape, or other consumer-facing contract:
   - Add `!` right after the type/component: `feat(config)!: drop support for the version 1 configuration format`
   - And add a `BREAKING CHANGE: <description>` footer at the end of the body explaining what breaks and how to migrate.

   **Component** (in parentheses) — required in commit and PR titles. Choose exactly one of `tui`, `assessment`, `compatibility`, `vsphere`, `docs`, `ci`, `release`, or `config` when it describes the main change. Use `other` when no single area fits; it deliberately adds no area label. Do not choose a component merely because the change mentions it or updates supporting documentation. The PR auto-label workflow reads this component, so do not use package names, paths, or file names as the title scope.

   **Summary** — imperative mood, lowercase, no period, ≤72 chars total for the first line. Describe what the change *does*, not what files changed.

   **Body** — include for any non-trivial change. Separate from the summary with a blank line. Use the following three sections:

   ```
   Changes:
   - <bullet describing each meaningful change; one line per logical unit>

   Root cause: <one or two sentences — WHY this change was needed (bug, design issue, obsolete dependency, broken test, etc.). Omit for pure feature additions; use "Motivation:" instead to explain the why.>

   Testing: <how correctness was verified — e.g., "go test ./... passed", "go test -race ./... passed, including tests/network_test.go", "manual run against vcsim", or "no tests — trivial config change">
   ```

   Omit the body only for single-line no-brainers (typo fix, rename, version bump). For everything else, always include all three sections.

   Do not add a `Co-Authored-By` (or similar) footer to the commit message. See **Authorship** above.

   **Examples:**
   ```
   fix(config): serialise prompt reads across concurrent connections

   Changes:
   - prompt.go: guard ReadLine and ReadSecret with a mutex
   - client.go: name the context in the prompt label

   Root cause: commands that span the estate connect to every context
   concurrently, so two goroutines wrote prompts and read answers through the
   same reader, interleaving the output and corrupting the input.

   Testing: go test -race ./... passed; previously reported a data race in
   credentials.(*Prompt).ReadSecret
   ```
   ```
   ci(ci): run gofmt, vet, staticcheck and tests on pushes and pull requests

   Changes:
   - ci.yml: add a build matrix over Linux, macOS and Windows
   - ci.yml: verify gofmt cleanliness and that go.mod is tidy
   - ci.yml: run the suite under -race and add a staticcheck job

   Motivation: nothing currently catches a broken build, an untidy go.mod, or a
   data race before it lands on main.

   Testing: workflow runs green on the PR that introduces it
   ```
   ```
   chore(other): bump govmomi to 0.56.0
   ```
   ```
   feat(config)!: require --context instead of falling back to the only context

   Changes:
   - root.go: remove the single-context fallback, require an explicit --context

   Motivation: the fallback silently picked a different vCenter once a second
   context was added; an explicit flag removes the ambiguity.

   BREAKING CHANGE: --context is now required when more than one context is
   configured. Scripts relying on the fallback will fail until they pass it.

   Testing: go test ./... passed, including internal/config
   ```

3. **Stage changes**

   Add specific files by name — never `git add -A` or `git add .` blindly.

   - List all modified/untracked files from `git status`
   - Skip files that look like secrets (`.env`, `*credentials*`, `*secret*`, `config.toml`) — warn the user instead
   - Stage everything else:
     ```bash
     git add <file1> <file2> ...
     ```

4. **Show the proposed commit and ask for confirmation**

   Help the user understand the proposed changes visually before asking for confirmation. Skip preamble and keep prose brief. Pick the smallest view that makes the key point clear:

   - **Show logic or an algorithm as pseudocode:**
     ```text
     on(Authenticate)
       if session token is valid
         return cached client
       prompt for credentials
       init soap session
       return authenticated client
     ```

   - **Show runtime control flow as a call tree:**
     ```text
     runSearch
       loadContexts
         parseConfig
       queryClustersConcurrently
         fetchVMs
         fetchHosts
       renderResultsTable
     ```

   - **Show UI/TUI structure as a component tree**, including state and module boundaries that matter:
     ```text
     <AppModel> (internal/tui/app.go)
       contextState (internal/uistate)
       <ContextListView>
         <SearchInput>
       <DetailPane>
         <VMTableView>
     ```

   - **Show file responsibility or a broad refactor as a shallow file tree:**
     ```text
     internal/
     ├── credentials/    # owns keyring and auth prompts
     ├── search/         # orchestrates concurrent queries
     └── transport/      # client connections and pool
     ```

   - **Show component interaction, control flow, or data flow with Mermaid:**
     ```mermaid
     sequenceDiagram
         participant CLI
         participant Auth
         participant vCenter
         CLI->>Auth: ResolveCredentials(context)
         Auth-->>CLI: User/Password
         CLI->>vCenter: Login(credentials)
         vCenter-->>CLI: SessionToken
     ```

   - **Use `diff` when the point is what changes and the surrounding shape already exists.** Match the diff shape to the topic:
     - For a component/view change:
       ```diff
        <AppModel>
          contextState
          <ContextListView>
       +    <SearchInput />
          <DetailPane>
       +    <VMTableView />
       ```
     - For a file-layout change:
       ```diff
        internal/
        ├── credentials/
       +│   └── keyring.go       # platform-native keyring storage
        ├── search/
       -└── transport.go
       +└── transport/
       +    ├── client.go
       +    └── pool.go
       ```
     - For a call-tree or call-stack change:
       ```diff
        runSearch
          loadContexts
            parseConfig
       +    validateCredentials
          queryClustersConcurrently
       -  renderResultsTable
       +  renderResultsTable
       +    highlightMatches
       ```
     - For a state or control-flow change:
       ```diff
        on(connect)
       -  dialEndpoint()
       +  if session.IsActive()
       +    return session.Client()
       +  dialEndpoint()
       +  persistSession()
       ```

   - **Show the whole block** when most of it is new, when omitted context would hide ownership or order, or when the user needs a copyable target shape:
     ```go
     func (s *SessionPool) Get(ctx context.Context, name string) (*Client, error) {
         if client, ok := s.active[name]; ok {
             return client, nil
         }
         return s.dialNew(ctx, name)
     }
     ```

   - **For a visual UI, layout, state comparison, or concept too dense for Mermaid**, write one focused HTML file — a diagram, an infographic, or a short slide deck, whichever fits the point. Match the product's colors, type, spacing, and components; use real labels and data; support desktop and mobile. Then open it for the user:
     ```bash
     open path/to/show-me-{description}.html
     ```

   **Visual summary guidance**:
   Place each visual next to the short text it supports. Keep only the calls, files, props, states, and boundaries needed to clarify what is being committed. You may use one of these, you may use several; it is unlikely you will use all of them. Use your judgement and don't overwhelm the user.

   Display:
   ```
   ## Proposed commit

   <full commit message>

   Files staged: <count>
   ```

   Use **AskUserQuestion tool** with options: "Commit", "Edit message", "Cancel"

   - If "Edit message": ask the user for their preferred message, then re-display and confirm once more
   - If "Cancel": stop, leave files staged
   - If "Commit": proceed

5. **Commit**

   ```bash
   git commit -m "$(cat <<'EOF'
   <type>(<component>): <summary>

   Changes:
   - <change 1>
   - <change 2>

   Root cause: <why>

   Testing: <how verified>
   EOF
   )"
   ```

   For trivial single-line commits (no body):
   ```bash
   git commit -m "<type>(<component>): <summary>"
   ```

6. **Display result**

   Run `git log --oneline -1` and show the commit hash + message.

   ```
   ## Committed

   <hash> <message>
   ```

7. **Offer to push and open a PR**

   Use **AskUserQuestion** with options: "Push & open PR", "Just commit (done)"

   If "Just commit (done)": stop here.

   If "Push & open PR":

   a. Check the current branch with `git branch --show-current`. If it's `main` (or the repo's default branch), create a new branch first — derive a short kebab-case name from the commit's `<type>(<component>)` (e.g. `fix/config-prompt-race`), or ask the user for one if nothing sensible can be derived:
      ```bash
      git checkout -b <branch-name>
      ```

   b. Push and set upstream:
      ```bash
      git push -u origin <branch-name>
      ```

   c. Build the PR title and body from the same material as the commit message — do not re-derive from scratch:
      - **Title**: the same conventional-commit format as step 2 — `<type>(<component>): <summary>` — identical to the commit's first line when the PR contains one commit. The `Auto label` check fails PR titles without a supported type and component. Supported types are `feat`, `fix`, `refactor`, `chore`, `docs`, `test`, `ci`, and `build`; supported components are listed in step 2. A breaking change may use `!` before the colon.
        Examples: `feat(vsphere): query every configured vCenter concurrently`, `fix(config): serialise prompt reads across concurrent connections`, `refactor(vsphere): resolve paths from one name/parent index`.
        When a PR carries several commits, the title describes the PR as a whole and still uses `<type>(<component>): <summary>`.
      - **Body** — reuse the commit's `Changes`, `Root cause`/`Motivation`, and `Testing` sections, and add a `Test plan` checklist (concrete, reviewer-actionable steps derived from `Testing`):

        ```
        ## Summary
        - <bullet from commit's Changes section>
        - <bullet from commit's Changes section>

        ## Root cause
        <or "## Motivation" — same content as the commit body>

        ## Test plan
        - [ ] <concrete step derived from Testing>
        - [ ] <concrete step derived from Testing>

        ## Testing
        <same content as the commit's Testing line>
        ```

        **Visual aids in PRs**: For non-trivial PRs (new features, architectural refactors, multi-package reorganizations, or state/control-flow changes), include a concise visual aid in the PR body (e.g. in `## Summary` or as a companion diagram) using the formats from Step 4 (such as a Mermaid sequence/architecture diagram, shallow file tree diff, or call-tree diff) to help reviewers understand the architectural delta at a glance.

      Never add a `Co-Authored-By`, "Generated with Claude Code", `Claude-Session`, or similar footer to the PR title or body. See **Authorship** above.

   d. Show the proposed PR title + body and confirm with **AskUserQuestion** ("Create PR", "Edit", "Cancel") before running:
      ```bash
      gh pr create --title "<title>" --body "$(cat <<'EOF'
      <body>
      EOF
      )"
      ```

      An agent without `gh` available uses the equivalent GitHub API tool, with the same title, the same body, and the same prior confirmation.

   e. Display the returned PR URL.

**Guardrails**
- Never use `git add -A` or `git add .`
- Never skip pre-commit hooks (`--no-verify`)
- Never commit `.env` or credential files — warn and skip them
- Never name an AI assistant as author, committer, or co-author, and never add an assistant attribution footer to a commit message, PR title, PR body, or PR comment
- If `git commit` fails due to a hook, report the hook output and stop; do not retry automatically
- If there are no changes to stage, report "Nothing to commit" and stop
- Always use HEREDOC syntax for multi-line commit messages and PR bodies to preserve formatting
- Never push directly to `main`/the default branch — always create a feature branch first
- Never force-push (`--force`/`-f`) as part of this flow
- Only run `gh pr create` after the user has explicitly confirmed the PR title/body
