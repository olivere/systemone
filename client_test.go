package systemone

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"math"
	"sync"
	"testing"
)

type testBackend struct {
	caps     Capabilities
	evaluate func(context.Context, Request) (Response, error)
}

func (b testBackend) Capabilities() Capabilities { return b.caps }
func (b testBackend) Evaluate(ctx context.Context, req Request) (Response, error) {
	return b.evaluate(ctx, req)
}

func allCapabilities() Capabilities { return Capabilities{Choice: true, Score: true, Noul: true} }

func fixture() (Request, Response) {
	return Request{
			State: jsontext.Value(`{"ticket":"Charged twice"}`),
			Questions: []QuestionSpec{
				{Key: "department", Kind: ChoiceKind, Instructions: "Which team?", Options: []Option[string]{Opt("billing", "Payments"), Opt("technical", "Bugs")}},
				{Key: "urgency", Kind: ScoreKind, Instructions: "How urgent?", Levels: []string{"Can wait", "Today", "Immediately"}},
				{Key: "refund", Kind: NoulKind, Instructions: "Is a refund requested?"},
			},
		}, Response{Model: "test-v1", Answers: map[string]Answer{
			"department": {Choice: &ChoiceAnswer{Choice: "billing", Probabilities: map[string]float64{"billing": .8, "technical": .2}, Confidence: new(.6)}, Source: Native},
			"urgency":    {Score: &ScoreAnswer{Score: 1.5, Probabilities: []float64{0, .5, .5}, Confidence: new(.2)}, Source: Estimated},
			"refund":     {Noul: &NoulAnswer{Probability: .9}},
		}}
}

func TestAskTypedHandles(t *testing.T) {
	t.Parallel()
	type team string
	department := MustChoice("department", "Which team?", Opt(team("billing"), "Payments"), Opt(team("technical"), "Bugs"))
	urgency := MustScore("urgency", "How urgent?", "Can wait", "Today", "Immediately")
	refund := MustNoul("refund", "Is a refund requested?").When("Refund requested", "No refund")
	_, response := fixture()
	client, err := New(Config{Backend: testBackend{caps: allCapabilities(), evaluate: func(_ context.Context, req Request) (Response, error) {
		if req.Questions[2].Yes != "Refund requested" {
			t.Fatal("missing outcome description")
		}
		return response, nil
	}}, Recorder: func(_ context.Context, o Observation) {
		if o.Err != nil {
			t.Error(o.Err)
		}
		o.Response.Answers["department"].Choice.Probabilities["billing"] = 0
	}})
	if err != nil {
		t.Fatal(err)
	}
	answers, err := client.Ask(t.Context(), map[string]string{"ticket": "Charged twice"}, department, urgency, refund)
	if err != nil {
		t.Fatal(err)
	}
	d, err := department.In(answers)
	if err != nil || d.Choice != team("billing") || d.Probabilities[team("billing")] != .8 || d.Source != Native {
		t.Fatalf("choice: %+v, %v", d, err)
	}
	s, err := urgency.In(answers)
	if err != nil || s.Score != 1.5 || s.Source != Estimated {
		t.Fatalf("score: %+v, %v", s, err)
	}
	n, err := refund.In(answers)
	if err != nil || n.Probability != .9 {
		t.Fatalf("noul: %+v, %v", n, err)
	}
	if answers.Model() != "test-v1" {
		t.Fatal("missing model metadata")
	}
	// Neither callers, observers, nor retained backend results own Answers data.
	d.Probabilities[team("billing")] = 0
	*d.Confidence = 0
	response.Answers["department"].Choice.Choice = "technical"
	d, err = department.In(answers)
	if err != nil || d.Choice != team("billing") || d.Probabilities[team("billing")] != .8 || *d.Confidence != .6 {
		t.Fatalf("results aliased: %+v %v", d, err)
	}
	other := MustChoice("department", "Which team?", Opt(team("billing"), "Payments"), Opt(team("technical"), "Bugs"))
	if _, err := other.In(answers); !errors.Is(err, ErrNotAsked) {
		t.Fatalf("different handle accepted: %v", err)
	}
	if _, err := department.In(Answers{}); !errors.Is(err, ErrNotAsked) {
		t.Fatalf("zero answers accepted: %v", err)
	}
}

func TestAskRejectsBeforeBackend(t *testing.T) {
	t.Parallel()
	q := MustNoul("q", "True?")
	client, err := New(Config{Backend: testBackend{caps: allCapabilities(), evaluate: func(context.Context, Request) (Response, error) { t.Fatal("backend called"); return Response{}, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []any{nil, true, 12, math.NaN(), make(chan int), jsontext.Value(`{"x":1,"x":2}`)} {
		if _, err := client.Ask(t.Context(), state, q); !errors.Is(err, ErrInvalidState) {
			t.Errorf("state %T: %v", state, err)
		}
	}
	for _, questions := range [][]Question{nil, {nil}, {(*Noul)(nil)}, {(*Score)(nil)}, {(*Choice[string])(nil)}, {Noul{}}, {q, q}, {q, MustNoul("q", "Other?")}} {
		if _, err := client.Ask(t.Context(), "state", questions...); !errors.Is(err, ErrInvalidQuestion) {
			t.Errorf("questions: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Ask(ctx, "state", q); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCapabilities(t *testing.T) {
	t.Parallel()
	req, _ := fixture()
	for _, caps := range []Capabilities{
		{},
		{Choice: true, Score: true},
		{Choice: true, Score: true, Noul: true, MaxOptions: 1},
		{Choice: true, Score: true, Noul: true, MaxLevels: 2},
		{Choice: true, Score: true, Noul: true, MaxQuestions: 2},
		{Choice: true, Score: true, Noul: true, MaxQuestions: -1},
	} {
		if err := CheckRequest(req, caps); !errors.Is(err, ErrUnsupported) {
			t.Errorf("caps %+v: %v", caps, err)
		}
	}
	if err := CheckRequest(req, allCapabilities()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateResponse(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Response){
		"missing":              func(r *Response) { delete(r.Answers, "refund") },
		"extra":                func(r *Response) { r.Answers["other"] = Answer{} },
		"wrong key":            func(r *Response) { r.Answers["other"] = r.Answers["refund"]; delete(r.Answers, "refund") },
		"wrong kind":           func(r *Response) { r.Answers["refund"] = r.Answers["department"] },
		"multiple kinds":       func(r *Response) { a := r.Answers["refund"]; a.Score = &ScoreAnswer{}; r.Answers["refund"] = a },
		"unknown option":       func(r *Response) { r.Answers["department"].Choice.Choice = "sales" },
		"nonmaximal option":    func(r *Response) { r.Answers["department"].Choice.Choice = "technical" },
		"nan confidence":       func(r *Response) { r.Answers["department"].Choice.Confidence = new(math.NaN()) },
		"invalid confidence":   func(r *Response) { r.Answers["urgency"].Score.Confidence = new(1.1) },
		"negative probability": func(r *Response) { r.Answers["department"].Choice.Probabilities["technical"] = -.1 },
		"nan probability":      func(r *Response) { r.Answers["department"].Choice.Probabilities["technical"] = math.NaN() },
		"missing option":       func(r *Response) { delete(r.Answers["department"].Choice.Probabilities, "technical") },
		"sum":                  func(r *Response) { r.Answers["department"].Choice.Probabilities["technical"] = .1 },
		"score mismatch":       func(r *Response) { r.Answers["urgency"].Score.Score = 1 },
		"score infinite":       func(r *Response) { r.Answers["urgency"].Score.Score = math.Inf(1) },
		"score nan":            func(r *Response) { r.Answers["urgency"].Score.Score = math.NaN() },
		"level missing":        func(r *Response) { r.Answers["urgency"].Score.Probabilities = []float64{.5, .5} },
		"noul invalid":         func(r *Response) { r.Answers["refund"].Noul.Probability = math.Inf(1) },
		"source invalid":       func(r *Response) { a := r.Answers["refund"]; a.Source = "calibrated"; r.Answers["refund"] = a },
		"negative usage":       func(r *Response) { r.Usage.InputTokens = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			req, response := fixture()
			mutate(&response)
			if err := ValidateResponse(req, response); !errors.Is(err, ErrInvalidResponse) {
				t.Fatal(err)
			}
		})
	}
	req, response := fixture()
	response.Answers["department"].Choice.Confidence = nil
	if err := ValidateResponse(req, response); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderOnErrorAndCancellation(t *testing.T) {
	t.Parallel()
	failure := errors.New("backend failed")
	var recorded error
	client, err := New(Config{Backend: testBackend{caps: allCapabilities(), evaluate: func(context.Context, Request) (Response, error) { return Response{}, failure }}, Recorder: func(_ context.Context, o Observation) { recorded = o.Err }})
	if err != nil {
		t.Fatal(err)
	}
	q := MustNoul("q", "True?")
	if _, err := client.Ask(t.Context(), "state", q); !errors.Is(err, failure) || !errors.Is(recorded, failure) {
		t.Fatalf("error %v, recorded %v", err, recorded)
	}
	if _, err := client.Ask(t.Context(), nil, q); !errors.Is(err, ErrInvalidState) || !errors.Is(recorded, ErrInvalidState) {
		t.Fatalf("error %v, recorded %v", err, recorded)
	}
}

func TestBackendCannotMutateQuestion(t *testing.T) {
	t.Parallel()
	q := MustChoice("q", "Which?", Opt("a", "First"), Opt("b", "Second"))
	client, err := New(Config{Backend: testBackend{caps: allCapabilities(), evaluate: func(_ context.Context, req Request) (Response, error) {
		req.Questions[0].Options[0].Value = "changed"
		return Response{Answers: map[string]Answer{"q": {Choice: &ChoiceAnswer{Choice: "a", Probabilities: map[string]float64{"a": 1, "b": 0}}}}}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := client.Ask(t.Context(), "state", q); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestConstructors(t *testing.T) {
	t.Parallel()
	if _, err := NewChoice("q", "Which?", Opt("same", ""), Opt("same", "")); !errors.Is(err, ErrInvalidQuestion) {
		t.Fatal(err)
	}
	if _, err := NewScore("q", "How much?", "single"); !errors.Is(err, ErrInvalidQuestion) {
		t.Fatal(err)
	}
	if _, err := NewNoul("", "True?"); !errors.Is(err, ErrInvalidQuestion) {
		t.Fatal(err)
	}
	if _, err := New(Config{}); err == nil {
		t.Fatal("nil backend accepted")
	}
	if _, err := New(Config{Backend: (*testBackend)(nil)}); err == nil {
		t.Fatal("typed nil backend accepted")
	}
	defer func() {
		if recover() == nil {
			t.Error("MustNoul should panic on invalid static schema")
		}
	}()
	MustNoul("", "")
}

func FuzzValidateResponse(f *testing.F) {
	f.Add(.5, .5, .5)
	f.Add(math.NaN(), math.Inf(1), -1.0)
	f.Fuzz(func(t *testing.T, yes, no, confidence float64) {
		req := Request{State: jsontext.Value(`"state"`), Questions: []QuestionSpec{{Key: "q", Kind: ChoiceKind, Instructions: "Which?", Options: []Option[string]{Opt("yes", ""), Opt("no", "")}}}}
		response := Response{Answers: map[string]Answer{"q": {Choice: &ChoiceAnswer{Choice: "yes", Probabilities: map[string]float64{"yes": yes, "no": no}, Confidence: &confidence}}}}
		if err := ValidateResponse(req, response); err == nil {
			if !probability(yes) || !probability(no) || !probability(confidence) || math.Abs(yes+no-1) > ProbabilityTolerance || no > yes+ProbabilityTolerance {
				t.Fatal("invalid numbers accepted")
			}
		}
	})
}
