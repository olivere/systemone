package systemone

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"math"
	"testing"
)

type invalidContentMarshaler struct{}

func (invalidContentMarshaler) MarshalJSON() ([]byte, error) { return []byte(`{"a":1,"a":2}`), nil }

func TestContentValidation(t *testing.T) {
	t.Parallel()
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, value := range []any{nil, true, 1, math.NaN(), "", " \n", "\xff", Content{}, invalidContentMarshaler{}, jsontext.Value("\"\xff\""), jsontext.Value(`null`), jsontext.Value(`{"a":1,"a":2}`), jsontext.Value(`[] []`), jsontext.Value(`{"a":`), cycle} {
		if _, err := NewContent(value); !errors.Is(err, ErrInvalidQuestion) {
			t.Errorf("accepted invalid content of type %T: %v", value, err)
		}
	}
	for _, value := range []any{"text", map[string]any{"focus": "primary request", "count": 2, "optional": nil}, []any{"a", 2}, jsontext.Value(`{"focus":"primary"}`)} {
		c, err := NewContent(value)
		if err != nil {
			t.Fatal(err)
		}
		if c.IsZero() || !c.JSON().IsValid() {
			t.Fatal("invalid content snapshot")
		}
		if _, err := json.Marshal(c); err != nil {
			t.Fatal(err)
		}
	}
	if !(Content{}).IsZero() || (Content{}).JSON() != nil {
		t.Fatal("invalid zero value")
	}
	if _, err := json.Marshal(Content{}); !errors.Is(err, ErrInvalidQuestion) {
		t.Fatal(err)
	}
}

func TestContentSnapshots(t *testing.T) {
	t.Parallel()
	value := map[string]any{"focus": []string{"primary"}}
	c := MustContent(value)
	before := string(c.JSON())
	value["focus"].([]string)[0] = "changed"
	data := c.JSON()
	data[0] = '['
	encoded, err := c.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	encoded[0] = '['
	if string(c.JSON()) != before {
		t.Fatal("content aliases mutable input or output")
	}
	raw := jsontext.Value(`{"focus":"primary"}`)
	c = MustContent(raw)
	raw[0] = '['
	if string(c.JSON()) != `{"focus":"primary"}` {
		t.Fatal("content aliases raw JSON")
	}
}

func TestContentQuestions(t *testing.T) {
	t.Parallel()
	instructions := MustContent(map[string]string{"question": "Which?", "focus": "primary request"})
	choice := MustChoiceContent("choice", instructions, OptContent("a", MustContent([]string{"first"})), Opt("b", "second"))
	levels := []Content{MustContent(map[string]string{"meaning": "low"}), MustContent("high")}
	score := MustScoreContent("score", instructions, levels...)
	levels[0] = MustContent("changed")
	noul := MustNoulContent("noul", instructions)
	withOutcomes, err := noul.WhenContent(MustContent("true case"), MustContent("false case"))
	if err != nil {
		t.Fatal(err)
	}
	caps := allCapabilities()
	caps.StructuredContent = true
	client, err := New(Config{Backend: testBackend{caps: caps, evaluate: func(_ context.Context, req Request) (Response, error) {
		if string(req.Questions[1].LevelContents[0].JSON()) != `{"meaning":"low"}` {
			t.Fatal("constructor retained input slice")
		}
		req.Questions[1].LevelContents[0] = MustContent("backend mutation")
		return Response{Answers: map[string]Answer{
			"choice": {Choice: &ChoiceAnswer{Choice: "a", Probabilities: map[string]float64{"a": 1, "b": 0}}},
			"score":  {Score: &ScoreAnswer{Score: .5, Probabilities: []float64{.5, .5}}},
			"noul":   {Noul: &NoulAnswer{Probability: .8}},
		}}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	answers, err := client.Ask(t.Context(), "state", choice, score, withOutcomes)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := choice.In(answers); err != nil || result.Choice != "a" {
		t.Fatalf("%+v %v", result, err)
	}
	result, err := score.In(answers)
	if err != nil || len(result.Levels) != 0 || len(result.LevelContents) != 2 {
		t.Fatalf("%+v %v", result, err)
	}
	result.LevelContents[0] = MustContent("result mutation")
	result, err = score.In(answers)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.LevelContents[0].JSON()) != `{"meaning":"low"}` {
		t.Fatal("result retained mutable slice")
	}
	if _, err := noul.In(answers); !errors.Is(err, ErrNotAsked) {
		t.Fatal(err)
	}
	plain := withOutcomes.When("yes", "no")
	if !plain.d.spec.YesContent.IsZero() || !plain.d.spec.NoContent.IsZero() {
		t.Fatal("When retained content")
	}
	if _, err := noul.WhenContent(Content{}, MustContent("no")); !errors.Is(err, ErrInvalidQuestion) {
		t.Fatal(err)
	}
	if _, err := (Noul{}).WhenContent(MustContent("yes"), MustContent("no")); !errors.Is(err, ErrInvalidQuestion) {
		t.Fatal(err)
	}
}

func TestContentRequestValidation(t *testing.T) {
	t.Parallel()
	c := MustContent("description")
	for _, q := range []QuestionSpec{
		{Key: "q", Kind: NoulKind, Instructions: "text", InstructionsContent: c},
		{Key: "q", Kind: NoulKind, Instructions: "text", Yes: "yes", YesContent: c},
		{Key: "q", Kind: ChoiceKind, Instructions: "text", Options: []Option[string]{{Value: "a", Description: "text", Content: c}, Opt("b", "")}},
		{Key: "q", Kind: ScoreKind, Instructions: "text", Levels: []string{"a", "b"}, LevelContents: []Content{c, c}},
		{Key: "q", Kind: ScoreKind, Instructions: "text", LevelContents: []Content{c, {}}},
		{Key: "q", Kind: ChoiceKind, Instructions: "text", Options: []Option[string]{Opt("a", ""), Opt("b", "")}, YesContent: c},
	} {
		if err := validateQuestion(q); !errors.Is(err, ErrInvalidQuestion) {
			t.Errorf("accepted %+v: %v", q, err)
		}
	}
	for _, q := range []QuestionSpec{
		{Key: "q", Kind: NoulKind, InstructionsContent: c},
		{Key: "q", Kind: NoulKind, Instructions: "text", YesContent: c},
		{Key: "q", Kind: NoulKind, Instructions: "text", NoContent: c},
		{Key: "q", Kind: ChoiceKind, Instructions: "text", Options: []Option[string]{OptContent("a", c), Opt("b", "")}},
		{Key: "q", Kind: ScoreKind, Instructions: "text", LevelContents: []Content{c, c}},
	} {
		req := Request{State: jsontext.Value(`{}`), Questions: []QuestionSpec{q}}
		caps := allCapabilities()
		if err := CheckRequest(req, caps); !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
		caps.StructuredContent = true
		if err := CheckRequest(req, caps); err != nil {
			t.Fatal(err)
		}
		if q.Kind == ScoreKind {
			caps.MaxLevels = 1
			if err := CheckRequest(req, caps); !errors.Is(err, ErrUnsupported) {
				t.Fatal(err)
			}
		}
	}
	if _, err := NewChoiceContent("q", Content{}, Opt("a", ""), Opt("b", "")); !errors.Is(err, ErrInvalidQuestion) {
		t.Fatal(err)
	}
	if _, err := NewScoreContent("q", c, c); !errors.Is(err, ErrInvalidQuestion) {
		t.Fatal(err)
	}
	if _, err := NewNoulContent("q", Content{}); !errors.Is(err, ErrInvalidQuestion) {
		t.Fatal(err)
	}
}

func FuzzContent(f *testing.F) {
	for _, seed := range []string{`"text"`, `{"focus":"primary"}`, `[]`, `null`, `{"a":1,"a":2}`, `""`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		c, err := NewContent(jsontext.Value(raw))
		if err != nil {
			if !errors.Is(err, ErrInvalidQuestion) {
				t.Fatal(err)
			}
			return
		}
		data := c.JSON()
		if !data.IsValid() || len(data) == 0 || (data[0] != '"' && data[0] != '{' && data[0] != '[') {
			t.Fatalf("accepted %q", raw)
		}
		if data[0] == '"' {
			var value string
			if err := json.Unmarshal(data, &value); err != nil || !nonempty(value) {
				t.Fatalf("accepted blank string %q", raw)
			}
		}
	})
}

func TestContentDomainJSON(t *testing.T) {
	t.Parallel()
	req, _ := fixture()
	if _, err := json.Marshal(req); err != nil {
		t.Fatalf("legacy request: %v", err)
	}
	q := QuestionSpec{Key: "q", Kind: ChoiceKind, InstructionsContent: MustContent(map[string]string{"focus": "primary"}), Options: []Option[string]{OptContent("a", MustContent(map[string]string{"meaning": "first"})), Opt("b", "second")}}
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		InstructionsContent map[string]string
		Options             []struct{ Content map[string]string }
	}
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if value.InstructionsContent["focus"] != "primary" || value.Options[0].Content["meaning"] != "first" {
		t.Fatalf("structured JSON = %s", data)
	}
}
