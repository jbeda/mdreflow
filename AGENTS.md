# Agent instructions for mdreflow

`docs/design.md` is canonical: read it before changing behavior, and land design changes there before code. `docs/m0-spike-findings.md` explains how dialect constructs map to goldmark's AST. `HANDOFF.md` (gitignored, may be absent) holds session-continuity notes.

## Navigate with gopls, not grep

gopls is installed.
For anything symbol-shaped, use it instead of grepping — it answers from the type-checked workspace (including dependencies and the stdlib), so it doesn't miss renames, embedding, or interface satisfaction the way text search does:

```
gopls workspace_symbol -matcher=fuzzy <name>   # find a symbol by (fuzzy) name
gopls symbols <file.go>                        # outline one file
gopls definition <file.go>:<line>:<col>        # jump to a definition
gopls references <file.go>:<line>:<col>        # every reference to the symbol
gopls implementation <file.go>:<line>:<col>    # interface <-> implementations
gopls call_hierarchy <file.go>:<line>:<col>    # callers and callees
gopls signature <file.go>:<line>:<col>         # signature + doc of a call
gopls check <file.go>                          # diagnostics for a file
```

Positions are `file:line:col` (1-based) or `file:#byte-offset`.
Get the line/col from a prior gopls result or a Read of the file.

Grep is still right for: strings and error-message text, comments, testdata fixtures, YAML/Markdown, and "does this pattern appear anywhere" questions.
Rule of thumb: identifiers → gopls; prose and literals → grep.

gopls also ships an MCP server (`gopls mcp`) if you'd rather wire these in as first-class tools.

## Subagents

If you spawn a subagent for implementation work in this repo, use Sonnet.
Do not escalate to Opus on your own initiative (Joe finds Opus 5 too verbose); reserve it for cases where Joe asked.

As the driver, feel free to plan the work up front and split it across multiple Sonnet subagents — parallel where tasks are independent, sequential where one stage feeds the next.
Keep review and integration in the driving session: verify each subagent's work (build, vet, lint, tests, and for reflow changes a fuzz soak) before building on it.

## Mechanical procedures live in Taskfile.yaml

Common procedures are code, not prompts: `task --list`.
Use `task verify`, `task fuzz`, `task release-verify VERSION=vX.Y.Z`, etc. rather than retyping the command chains; if you find yourself repeating a mechanical procedure that isn't there, add it.
Releases follow RELEASING.md.
Every user-visible change adds a line to CHANGELOG.md's Unreleased section in the same commit, written in user terms.

Any change to the user-facing surface — a flag, a config key, an exit code, a public API symbol, a dialect — updates **all three doc surfaces in the same commit**: `--help` (cmd/mdreflow/usage.go, the complete agent-facing reference), README.md (the front door; its Configuration block presents itself as the complete key list, so an omission there is a false claim), and CHANGELOG.md.
The #19 merge shipped `--dialect` in help and changelog but not the README — this rule exists so that class of drift is caught at commit time, not by a reader.

## Verify before declaring done

```
go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1
golangci-lint run
```

CI runs golangci-lint too — run it locally before considering work done, or the push fails a check you never saw.

`testdata/` goldens are byte-exact (`.gitattributes -text`) — never let an editor or git normalize their line endings.
Fuzz seeds live in `testdata/fuzz/FuzzFormat/`; when a fuzz failure is fixed, check the seed in.
