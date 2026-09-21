# Contributing to goat

Thank you for helping improve goat. Bug reports, documentation fixes, tests, and code contributions are welcome.

## Before you start

- Search existing issues and pull requests to avoid duplicate work.
- Open an issue before making a large change or changing a public API.
- Do not include API keys, credentials, private conversation data, or other secrets in issues, tests, or commits.
- Report security vulnerabilities according to [SECURITY.md](SECURITY.md), not in a public issue.

## Development setup

The minimum supported Go version is declared in [`go.mod`](go.mod). Clone the repository and download its dependencies:

```bash
git clone https://github.com/torrischen/goat.git
cd goat
go mod download
```

Run the same core checks used by CI:

```bash
gofmt -w .
go vet ./...
go test -race ./...
```

To run the vulnerability scanner locally:

```bash
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```

Some integrations require external services or provider credentials. Tests submitted in a pull request should not depend on a contributor's credentials. Prefer local fakes or `httptest.Server`; clearly document any optional integration-test setup.

When changing the gRPC plugin protocol, regenerate its bindings:

```bash
make proto
```

Commit generated bindings together with the protocol change.

## Making a change

1. Create a focused branch from the default branch.
2. Add or update tests for behavior changes.
3. Add GoDoc for new exported identifiers.
4. Keep public API changes backward compatible where practical.
5. Update the relevant README and the `Unreleased` section of [CHANGELOG.md](CHANGELOG.md).
6. Run the checks above before opening a pull request.

Use clear commit messages that explain the intent of the change. Keep unrelated refactors in separate pull requests.

## Pull requests

A pull request should explain:

- what changed and why;
- how it was tested;
- whether it changes public APIs, persisted data, configuration, or dependencies;
- any security or compatibility implications.

By participating in this project, you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
