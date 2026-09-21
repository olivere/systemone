# Contributing

Open an issue before adding a dependency, a backend, or a large public API change.
Keep changes focused. Preserve the standard-library-only implementation and the
separation between model configuration and application decisions.

Use Go 1.27 or newer. Before submitting a pull request, run:

```sh
gofumpt -w .
go build ./...
go test -race -count=1 -timeout=5m ./...
go vet ./...
staticcheck ./...
govulncheck ./...
```

The CI workflow pins the check-tool versions. Tests must not require API keys
or paid model calls. Use `sonetest` or an in-memory HTTP server for deterministic
tests. Include regression tests for fixes and examples for new public APIs.

Keep `mise.toml` local. Never include credentials, customer data, or unredacted
HTTP dumps in commits, issues, or pull requests. See [SECURITY.md](SECURITY.md)
for vulnerability reports.

Use a feature branch and a short, descriptive pull request title. Pull requests
are squash-merged; the title becomes the commit subject. Submissions are covered
by the repository's [MIT license](LICENSE).
