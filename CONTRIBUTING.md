# Contribution Guidelines

We accept pull requests, bug fixes and issue reports. However, this fork of
QuickFIX/Go is intended primarily for internal use within Appital, so we can
give no assurance of how long it will take us to read and respond to them.

For general questions about FIX or QuickFIX/Go concepts, the upstream
[QuickFIX/Go documentation](https://quickfixengine.org/go/documentation/) is the
best place to start.

## Before You Start

- For anything beyond a small fix, please open an issue describing the problem
  and your proposed change before writing code. We may not want the change, or
  may want it done differently, and agreeing first saves wasted effort.
- Changes that alter the public API or existing behaviour are possible, as gofix
  does not make upstream's compatibility guarantees, but they need a clear
  justification.

## Submitting Changes

- Push your changes to a topic branch in your fork of the repository.
- Open a pull request against the `main` branch of this repository.
- Every change, including those from maintainers, is reviewed through a pull
  request before it is merged. Be prepared to respond to review comments.
- Keep pull requests small and focused on one change. Unrelated refactors,
  formatting or dependency bumps belong in separate pull requests.

## Commit Messages and Pull Request Titles

We vaguely but not rigorously use [Scoped Commits](https://scopedcommits.com/)
rather than Conventional Commits. The subject line names the area of the code
that changed, followed by a short description, e.g.:

```
<scope>: <description>

<optional body explaining why the change is needed>

<optional trailers>
```

- The scope is the package or area touched, for example `acceptor`, `session`,
  `datadictionary`, `store/sql`, `cmd/generate-fix`, `metrics/prometheus`,
  `docs` or `ci`.
- Use `all` for changes that span the whole tree. If a change touches two areas,
  list both separated by a comma, or pick a broader scope.
- Write the description as a short imperative summary, for example
  `session: Stop allocating on resend requests`.
- Do not use Conventional Commits types such as `feat:` or `fix:`.
- Pull request titles follow the same format, since they become the commit
  subject when the pull request is squash-merged.

## What We Expect in a Change

- **Tests.** All code changes should come with tests. If you report a bug and
  do not include a fix, please include a failing test. Contributions without
  tests will generally not be accepted.
- **A green build.** `make` (vet and unit tests) and `make lint` should pass
  locally, and the CI build must pass. Contributions that fail the automated
  build will not be accepted.
- **Benchmarks for performance changes.** Changes to parsing, validation,
  message building or the session hot path should include before and after
  measurements from `make bench-baseline` and `make bench-compare`. See
  [docs/BENCHMARKING.md](docs/BENCHMARKING.md).
- **Documentation.** Update the README, `docs/` or Go doc comments when
  behaviour or configuration changes, and add an entry to
  [CHANGELOG.md](CHANGELOG.md) for user-visible changes. Breaking changes go
  under `BREAKING CHANGES`.
- **No generated code.** The packages in `gen/` are generated and not checked
  in. To change them, change the generator in `cmd/generate-fix`.

## AI-Assisted Contributions

AI-assisted contributions are welcome, but the person submitting them is
responsible for the result. See [AGENTS.md](AGENTS.md) for the rules that apply
to coding agents working in this repository.

## Security Issues

Please do not report security vulnerabilities in public issues. See
[SECURITY.md](SECURITY.md).
