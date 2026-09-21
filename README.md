# systemone

Small, standard-library-only Go library for typed AI decisions. Requires Go 1.27+.

Define questions once, evaluate them together, and read results through the same
typed handles. Application code uses `systemone`; model selection and credentials
belong in backend setup. The `jev` package supports TypeSafe's HTTP API.

```sh
go get github.com/olivere/systemone
```

## Use Jev

For local development with [mise](https://mise.jdx.dev/environments/), copy
`mise.example.toml` to `mise.toml` if the local file does not exist. Set
`TYPESAFE_API_KEY` in `mise.toml`, which is git-ignored, then run `mise trust`.
An activated mise shell loads the variable automatically; use `mise exec -- <command>`
otherwise. The library receives the key explicitly as shown below.

Run the included [live example](examples/jev/main.go) from this repository:

```sh
mise exec -- go run ./examples/jev
mise exec -- go run ./examples/jev -text "Our integration has been down all morning."
```

Each run makes one real TypeSafe API request and prints all three decisions.
Use `-model` to select a model version; the default is `jev-latest`.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/olivere/systemone"
	"github.com/olivere/systemone/jev"
)

type Team string

const (
	Billing   Team = "billing"
	Technical Team = "technical"
)

var (
	Department = systemone.MustChoice("department", "Which team should handle this ticket?",
		systemone.Opt(Billing, "Payments, invoicing, and refunds"),
		systemone.Opt(Technical, "Bugs, outages, and integrations"),
	)
	Urgency = systemone.MustScore("urgency", "How soon does this ticket need attention?",
		"Can wait until next week", "Needs attention today", "Blocked and needs attention now",
	)
	WantsRefund = systemone.MustNoul("refund", "Does the customer request a refund?")
)

func main() {
	backend, err := jev.New(jev.Config{APIKey: os.Getenv("TYPESAFE_API_KEY")})
	if err != nil {
		log.Fatal(err)
	}
	client, err := systemone.New(systemone.Config{Backend: backend})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	answers, err := client.Ask(ctx, "I was charged twice. Please refund the duplicate.",
		Department, Urgency, WantsRefund)
	if err != nil {
		log.Fatal(err)
	}
	department, err := Department.In(answers)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(department.Choice, department.Probabilities, answers.Model())
}
```

State can be a string, struct, map, slice, or `jsontext.Value` that encodes as a
JSON string, object, or array.
The library rejects null, boolean, and numeric top-level state.

Use `NewChoice`, `NewScore`, and `NewNoul` for runtime definitions; they return
errors. `Must*` constructors panic for malformed static definitions. Choices
accept named string types without a registry or schema reflection.

`In` returns a typed result and an error. A successful `Ask` verifies every
requested answer, but cannot prevent a later call with an unrelated handle.
That call returns `ErrNotAsked`, even if the other handle has the same key.
Copying a handle preserves its identity. `Noul.When(yes, no)` creates a new
handle; use that returned handle for both `Ask` and `In`.

## Structured instructions and criteria

Use the additive `NewChoiceContent`, `NewScoreContent`, and `NewNoulContent`
constructors (or their `Must*` forms) for JSON instructions. Existing string
constructors remain available. `NewContent` serializes and validates a string,
object, or array; `MustContent` panics on invalid static content. Pass Go maps,
structs, or slices, or use `jsontext.Value` for raw JSON. A plain Go string is
always text, never parsed as JSON.

```go
department := systemone.MustChoiceContent("department",
	systemone.MustContent(map[string]string{
		"question": "Which team should handle `ticket.message`?",
		"focus":    "Classify the current request, not the account history.",
	}),
	systemone.OptContent(Billing, systemone.MustContent(map[string]any{
		"description": "Payments and refunds",
		"examples":    []string{"Duplicate charge", "Refund request"},
	})),
	systemone.Opt(Technical, "Bugs and outages"),
)
```

`focus` is part of the instructions sent to the model, not a library setting.
Structured content follows the [TypeSafe API contract](https://docs.typesafe.ai/api).
Content snapshots its input: later changes to a source map or slice do not change
the question. Top-level null, booleans, numbers, and blank strings are rejected,
as are invalid JSON, duplicate object keys, and invalid UTF-8. Nested JSON values
may include numbers, booleans, and null.

`OptContent` adds structured choice descriptions and can be mixed with `Opt`.
`MustScoreContent` accepts ordered `Content` levels; results expose them through
`ScoreResult.LevelContents` instead of the string-only `Levels` field.
`Noul.WhenContent` returns a new handle with structured true/false descriptions
and an error. Use the returned handle for both `Ask` and `In`.

The [executable example](example_test.go) uses a scripted backend with no API key.
The [live example](examples/structured/main.go) shows all three question kinds,
object and array criteria, and nested input state:

```sh
go test -run ExampleClient_Ask_structuredContent .
mise exec -- go run ./examples/structured
mise exec -- go run ./examples/structured -text "The API is down." -focus "Route by the immediate business problem."
```

Each live run makes one paid API request. Custom backends must declare
`Capabilities.StructuredContent` and handle the content fields in `QuestionSpec`;
otherwise `Check` and `Ask` return `ErrUnsupported` before evaluating them.

## What the numbers mean

| Question | Result |
| --- | --- |
| Choice | One supplied label, its distribution, optional confidence |
| Score | Expected zero-based level, its distribution, optional confidence |
| Noul | Probability that the statement is true |

A three-level Score ranges from 0 to 2 and can be fractional. All distributions
must be present. Validation allows rounding error of `ProbabilityTolerance`
(0.001) in their sum and that tolerance times the level count in expected scores.
Individual probabilities must remain within [0,1]. Values are never normalized.
Confidence is optional in the domain contract; the Jev adapter requires it for
Choice and Score because the TypeSafe protocol does.

Results report `Source` as `Native`, `Estimated`, or `Unspecified`. This describes
how probabilities were obtained. It says nothing about calibration. Jev's
confidence is derived from its distribution and is not P(correct). The library
does not set a `Calibrated` flag: calibration needs evidence for your model
version, language, question, and input population. Fit and test thresholds before
using them to authorize application actions.

Question handles and `Answers` support concurrent reads. Returned
maps, slices, and confidence pointers are copies. Do not mutate input state
while `Ask` serializes it. Custom backends and recorder callbacks must also be
safe for concurrent use.

## Other backends

Implement this interface in your application or another package:

```go
type Backend interface {
	Capabilities() systemone.Capabilities
	Evaluate(context.Context, systemone.Request) (systemone.Response, error)
}
```

The request carries a JSON state and domain questions, with no HTTP envelope,
credentials, or model identifier. A backend can use a local model service or an
LLM adapter. Generated probability estimates should use `Estimated`; don't turn
a discrete prediction into a one-hot distribution that claims certainty.

`client.Check(Department, Urgency, WantsRefund)` checks schemas and declared
capabilities without a network call. `Ask` checks them again. Zero ceilings mean
no declared limit, not unlimited model capacity or guaranteed accuracy.

`jev.Config` accepts a custom `BaseURL`, `Model`, `HTTPClient`, `Capabilities`, and
`ProbabilitySource`. The base URL is an origin or path prefix; the adapter appends
`/v1/systemone`. API keys are optional for custom servers. TypeSafe defaults are
`https://api.typesafe.ai`, `jev-latest`, 255 Choice options, and 10 Score levels.
Custom endpoints default to no declared ceilings and `Unspecified` probability
source. Pin a model version when evaluating thresholds.

The default HTTP client has a ten-second timeout. An injected client retains its
timeout and transport; pass a context deadline when it has no timeout. Redirects
are disabled even on injected clients. Responses are limited to 8 MiB. Errors
preserve cancellation and expose non-2xx status via `*jev.HTTPError`, without
including server response bodies. There are no automatic retries or fallbacks.

### HTTP debugging

Both live examples accept `-debug` to dump HTTP requests and responses to stderr:

```sh
mise exec -- go run ./examples/jev -debug
mise exec -- go run ./examples/structured -debug
```

In your application, set `jev.Config.Debug` to a writer:

```go
backend, err := jev.New(jev.Config{
	APIKey: os.Getenv("TYPESAFE_API_KEY"),
	Debug:  os.Stderr,
})
```

Dumps include the request method and URL, response status, headers, and bodies,
including error responses. Only allowlisted protocol headers retain their values;
authorization, cookies, and other headers are redacted. Bodies are **not redacted**
and can contain customer data or credentials echoed by a server. Use this only
with data you are comfortable logging. Body dumps stop at 64 KiB and mark truncation.

Debugging is off by default. Writes are synchronous and serialized per backend;
use a writer that returns promptly. If several backends share a writer, it must
support concurrent writes. Debug write failures do not change evaluation results.

Several OpenJev projects expose the same endpoint. Their compatibility is a
candidate for testing, not a claim that this release has run against them. Laya
currently needs a separately specified serving wrapper; this repository includes
no Python sidecar or Laya adapter.

## Recording and shadow comparisons

Set `Config.Recorder` to receive the duration, response snapshot, and error for
each `Ask`. It runs synchronously, including on failures, so keep it short.
It receives no input state. Use answer keys to collect distributions and
confidence histograms. The library logs nothing by default.

`NewShadow(applicationContext, ShadowConfig{...})` wraps a primary backend and a
candidate. Supply both backends, a positive `Timeout`, and a `Report` callback.
`MaxConcurrent` defaults to one and bounds all pending/running comparisons,
including callbacks. A successful primary evaluation returns without waiting for
the candidate. `Dropped()` counts comparisons skipped when capacity is exhausted
or comparisons have stopped.

Candidate work uses the application's lifetime and its own timeout, independently
of the request deadline. `Report` receives both response snapshots, the request,
and any candidate error. It reports every completed comparison, not just changed
labels, so you can evaluate score and probability shifts too. Its context shares
the candidate deadline and may already be cancelled. Callbacks must return
promptly and honor cancellation. Recording requires your own data-retention policy.

Call `Close()` at shutdown to cancel comparisons, release queued states, and wait
for workers. Backends that ignore cancellation or blocked callbacks can delay
shutdown. A callback must not call `Close` itself. The primary remains available
after shadow comparisons stop.

## Test application decisions

`sonetest.New(sonetest.Config{Steps: ...})` returns a concurrency-safe scripted
backend. Each uncanceled evaluation consumes one `Step{Response, Err}`.
`Calls()` returns request snapshots, `Remaining()` reports unconsumed steps, and
an exhausted script returns `sonetest.ErrExhausted`. It accepts malformed responses
so consumers can test rejection paths. See the executable
[escalation example](sonetest/fake_test.go).

```sh
go test -race ./...
go vet ./...
staticcheck ./...
govulncheck ./...
gofumpt -l .
```

The test suite uses scripted backends and in-memory HTTP servers. It does not
require credentials or make paid model calls. Live latency, accuracy, and
calibration have not been measured by this package.

## Protocol references

- [TypeSafe HTTP API](https://docs.typesafe.ai/api)
- [Score semantics](https://docs.typesafe.ai/primitives/score)
- [Confidence semantics](https://docs.typesafe.ai/confidence)
- [TypeSafe's LLM adapter](https://github.com/typesafe-ai/system-one-adapter-python)
- [OpenJev HTTP server](https://github.com/razorback16/openjev)
- [OpenJev SGLang server](https://github.com/ekzhang/openjev-sglang)
- [Laya Python implementation](https://github.com/NandhaKishorM/laya)
