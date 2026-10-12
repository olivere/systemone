# Changelog

All notable changes to this module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Before v1.0.0, a minor version may
contain breaking changes.

## [v0.1.0] - 2026-10-12

First tagged release. Requires Go 1.27.2 or newer.

### Added

- Typed questions: `Choice` over a named string type, `Score` over ordered
  levels, and `Noul` for the probability that a statement is true. Each
  question type has a `New*` constructor that returns an error and a `Must*`
  constructor for static definitions.
- `Client.Ask` evaluates several independent questions against one JSON state
  in a single backend call. `Client.Check` validates schemas and backend
  capabilities without a network call.
- Structured instructions and criteria through `Content`, with the
  `NewChoiceContent`, `NewScoreContent`, `NewNoulContent`, `OptContent` and
  `Noul.WhenContent` constructors.
- Strict response validation: every distribution must be present, sum to one
  within `ProbabilityTolerance`, and agree with the selected choice or the
  expected score.
- `Config.Recorder` for per-call observations, and `NewShadow` for comparing a
  candidate backend against the primary without delaying responses.
- `sonetest`, a scripted backend for testing application decisions without
  API keys.
- `jev` backend for TypeSafe AI and compatible OpenJev servers.
- `openai` backend for the OpenAI Decisions API (`POST /v1/decisions`, public
  beta). It maps `Choice`, `Score` and `Noul` to `choice`, `score` and
  `predicate` questions, declares the API's limits of 255 choices, 10 levels
  and 200 questions, fails with `openai.ErrRefused` when the model refuses a
  question, and reports error type, code and request ID in `*openai.HTTPError`.
- `Debug` writer on both backends to dump HTTP traffic. Headers outside an
  allowlist are redacted; bodies are not.
- Live examples in `examples/jev`, `examples/structured` and `examples/openai`.

### Security

- With an API key, both backends accept a plain `http` base URL only for a
  loopback IP address such as `127.0.0.1` or `[::1]`. The name `localhost` is
  rejected, because Go resolves it through DNS when `/etc/hosts` has no entry,
  which would send the key off the machine in cleartext. Keyless custom `jev`
  servers may still use plain `http` under any host name.
- Both backends disable redirects on their HTTP client so the API key never
  follows a 3xx, cap response bodies at 8 MiB, and leave server error messages
  out of returned errors.

### Changed since the untagged September snapshot

If you depend on a pseudo-version from before this release, note one breaking
change: `jev.New` now rejects an API key together with a non-loopback `http`
base URL, including `http://localhost`. Use `https`, use `http://127.0.0.1`, or
drop the key if the server does not need one.

[v0.1.0]: https://github.com/olivere/systemone/releases/tag/v0.1.0
