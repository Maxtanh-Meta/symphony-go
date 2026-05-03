# minisymphony

A small Go orchestrator that drives Codex or Claude Code on GitHub issues
labeled `symphony:ready`, posts a plan, decides approval (rules + reviewer
agent + post-impl diff verification, or human, or none), implements,
validates, and opens a draft PR.

See [`SPEC.md`](./SPEC.md) for the full design.

## Status

Pre-alpha. Skeleton only. Implementation milestones in `SPEC.md` §15.

## Quick start (once implemented)

```sh
# 1. Install
go install ./cmd/minisymphony

# 2. Create config (lives outside the repo on purpose)
mkdir -p ~/.minisymphony
cp testdata/config.example.yml ~/.minisymphony/config.yml
# edit repo.full_name, repo.local_path, etc.

# 3. Add WORKFLOW.md prompt template inside your repo
cp testdata/WORKFLOW.example.md /path/to/repo/WORKFLOW.md

# 4. Verify the setup
GITHUB_TOKEN=ghp_... minisymphony doctor --config ~/.minisymphony/config.yml

# 5. Run
GITHUB_TOKEN=ghp_... minisymphony run --config ~/.minisymphony/config.yml
```

## Module path

The Go module path is `github.com/logosc/symphony-go`. To fork under a
different organization, run `go mod edit -module ...` and update imports.

## License

TBD.
