package sonetest_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/olivere/systemone"
	"github.com/olivere/systemone/sonetest"
)

func ExampleFake() {
	urgent := systemone.MustNoul("urgent", "Does the customer need immediate help?")
	backend := sonetest.New(sonetest.Config{Steps: []sonetest.Step{{
		Response: systemone.Response{Answers: map[string]systemone.Answer{
			"urgent": {Noul: &systemone.NoulAnswer{Probability: 0.95}},
		}},
	}}})
	client, err := systemone.New(systemone.Config{Backend: backend})
	if err != nil {
		fmt.Println(err)
		return
	}
	answers, err := client.Ask(context.Background(), "Our production service is down.", urgent)
	if err != nil {
		fmt.Println(err)
		return
	}
	result, err := urgent.In(answers)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("escalate:", result.Probability >= 0.9)
	// Output: escalate: true
}

func TestScriptOrderCancellationAndExhaustion(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("backend unavailable")
	fake := sonetest.New(sonetest.Config{Steps: []sonetest.Step{
		{Response: systemone.Response{Model: "first"}},
		{Err: wantErr},
	}})
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fake.Evaluate(canceled, systemone.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled evaluation: %v", err)
	}
	if fake.Remaining() != 2 || len(fake.Calls()) != 0 {
		t.Fatal("canceled evaluation consumed a step or recorded a call")
	}
	first, err := fake.Evaluate(t.Context(), systemone.Request{})
	if err != nil || first.Model != "first" {
		t.Fatalf("first evaluation: %+v, %v", first, err)
	}
	if _, err := fake.Evaluate(t.Context(), systemone.Request{}); !errors.Is(err, wantErr) {
		t.Fatalf("second evaluation: %v", err)
	}
	if _, err := fake.Evaluate(t.Context(), systemone.Request{}); !errors.Is(err, sonetest.ErrExhausted) {
		t.Fatalf("exhausted evaluation: %v", err)
	}
	if fake.Remaining() != 0 || len(fake.Calls()) != 3 {
		t.Fatal("unexpected script position or call count")
	}
}

func TestSnapshots(t *testing.T) {
	t.Parallel()
	response := systemone.Response{Answers: map[string]systemone.Answer{
		"choice": {Choice: &systemone.ChoiceAnswer{Choice: "a", Probabilities: map[string]float64{"a": 0.8}, Confidence: new(0.6)}},
		"score":  {Score: &systemone.ScoreAnswer{Score: 0.7, Probabilities: []float64{0.3, 0.7}, Confidence: new(0.2)}},
		"noul":   {Noul: &systemone.NoulAnswer{Probability: 0.9}},
	}}
	fake := sonetest.New(sonetest.Config{Steps: []sonetest.Step{{Response: response}, {Response: response}}})
	response.Answers["choice"].Choice.Probabilities["a"] = 0
	*response.Answers["choice"].Choice.Confidence = 0
	response.Answers["score"].Score.Probabilities[0] = 0
	*response.Answers["score"].Score.Confidence = 0
	response.Answers["noul"].Noul.Probability = 0
	delete(response.Answers, "score")
	request := systemone.Request{State: []byte(`{}`), Questions: []systemone.QuestionSpec{{
		Key: "q", Options: []systemone.Option[string]{{Value: "a"}}, Levels: []string{"low"},
	}}}
	first, err := fake.Evaluate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Answers["choice"].Choice.Probabilities["a"] != 0.8 || *first.Answers["choice"].Choice.Confidence != 0.6 || first.Answers["score"].Score.Probabilities[0] != 0.3 || *first.Answers["score"].Score.Confidence != 0.2 || first.Answers["noul"].Noul.Probability != 0.9 {
		t.Fatal("script retained mutable caller data")
	}
	first.Answers["choice"].Choice.Probabilities["a"] = 0
	*first.Answers["choice"].Choice.Confidence = 0
	first.Answers["score"].Score.Probabilities[0] = 0
	*first.Answers["score"].Score.Confidence = 0
	first.Answers["noul"].Noul.Probability = 0
	second, err := fake.Evaluate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Answers["choice"].Choice.Probabilities["a"] != 0.8 || *second.Answers["choice"].Choice.Confidence != 0.6 || second.Answers["score"].Score.Probabilities[0] != 0.3 || *second.Answers["score"].Score.Confidence != 0.2 || second.Answers["noul"].Noul.Probability != 0.9 {
		t.Fatal("returned response aliases another step")
	}
	request.State[0] = '['
	request.Questions[0].Key = "changed"
	request.Questions[0].Options[0].Value = "changed"
	request.Questions[0].Levels[0] = "changed"
	calls := fake.Calls()
	if string(calls[0].State) != `{}` || calls[0].Questions[0].Key != "q" || calls[0].Questions[0].Options[0].Value != "a" || calls[0].Questions[0].Levels[0] != "low" {
		t.Fatal("recorded request aliases caller data")
	}
	calls[0].State[0] = '['
	calls[0].Questions[0].Options[0].Value = "changed"
	calls[0].Questions[0].Levels[0] = "changed"
	if !reflect.DeepEqual(fake.Calls()[0], fake.Calls()[1]) {
		t.Fatal("Calls exposed mutable recorded requests")
	}
}

func TestCapabilities(t *testing.T) {
	t.Parallel()
	if got := sonetest.New(sonetest.Config{}).Capabilities(); !got.Choice || !got.Score || !got.Noul {
		t.Fatalf("default capabilities: %+v", got)
	}
	caps := systemone.Capabilities{Choice: true, MaxOptions: 3}
	fake := sonetest.New(sonetest.Config{Capabilities: &caps})
	caps.MaxOptions = 100
	if got := fake.Capabilities(); got.MaxOptions != 3 || got.Score || got.Noul {
		t.Fatalf("configured capabilities: %+v", got)
	}
}

func TestConcurrentEvaluation(t *testing.T) {
	t.Parallel()
	const count = 32
	steps := make([]sonetest.Step, count)
	for i := range steps {
		steps[i].Response.Model = fmt.Sprint(i)
	}
	fake := sonetest.New(sonetest.Config{Steps: steps})
	results := make(chan string, count)
	var workers sync.WaitGroup
	for range count {
		workers.Go(func() {
			response, err := fake.Evaluate(t.Context(), systemone.Request{})
			if err != nil {
				t.Error(err)
				return
			}
			results <- response.Model
			_ = fake.Calls()
			_ = fake.Remaining()
		})
	}
	workers.Wait()
	close(results)
	seen := make(map[string]bool)
	for result := range results {
		if seen[result] {
			t.Fatalf("step %s returned twice", result)
		}
		seen[result] = true
	}
	if len(seen) != count || len(fake.Calls()) != count || fake.Remaining() != 0 {
		t.Fatal("concurrent evaluations lost steps or calls")
	}
}
