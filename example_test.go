package systemone_test

import (
	"context"
	"fmt"

	"github.com/olivere/systemone"
	"github.com/olivere/systemone/sonetest"
)

func ExampleClient_Ask() {
	type Team string
	const billing Team = "billing"
	const technical Team = "technical"
	department := systemone.MustChoice("department", "Which team should handle this ticket?",
		systemone.Opt(billing, "Payments and refunds"),
		systemone.Opt(technical, "Bugs and outages"))
	backend := sonetest.New(sonetest.Config{Steps: []sonetest.Step{{
		Response: systemone.Response{Answers: map[string]systemone.Answer{
			"department": {
				Choice: &systemone.ChoiceAnswer{
					Choice: "billing", Probabilities: map[string]float64{"billing": .9, "technical": .1},
				},
			},
		}},
	}}})
	client, err := systemone.New(systemone.Config{Backend: backend})
	if err != nil {
		fmt.Println(err)
		return
	}
	answers, err := client.Ask(context.Background(), "I was charged twice.", department)
	if err != nil {
		fmt.Println(err)
		return
	}
	result, err := department.In(answers)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(result.Choice, result.Probabilities[billing])
	// Output: billing 0.9
}

func ExampleClient_Ask_structuredContent() {
	type Team string
	const billing Team = "billing"
	const technical Team = "technical"
	department := systemone.MustChoiceContent(
		"department",
		systemone.MustContent(map[string]any{
			"question": "Which team should handle `ticket.message`?",
			"focus":    "Classify the customer's current request, not the account history.",
		}),
		systemone.OptContent(billing, systemone.MustContent(map[string]any{
			"description": "Payments and refunds",
			"examples":    []string{"Duplicate charge", "Refund request"},
		})),
		systemone.Opt(technical, "Bugs and outages"),
	)
	urgency := systemone.MustScoreContent(
		"urgency",
		systemone.MustContent(map[string]string{
			"question": "How soon does `ticket.message` need attention?",
			"focus":    "Use the customer's stated deadline and business impact.",
		}),
		systemone.MustContent(map[string]string{"description": "The request can wait until next week."}),
		systemone.MustContent(map[string]string{"description": "The request needs attention today."}),
	)
	refund, err := systemone.MustNoulContent(
		"refund",
		systemone.MustContent([]string{
			"Does the customer request a refund in `ticket.message`?",
			"Ignore previous refunds in `account`.",
		}),
	).WhenContent(
		systemone.MustContent(map[string]string{"description": "The customer asks to return a payment."}),
		systemone.MustContent(map[string]string{"description": "The customer does not ask to return a payment."}),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	// Scripted answers keep this example deterministic and require no API key.
	backend := sonetest.New(sonetest.Config{Steps: []sonetest.Step{{
		Response: systemone.Response{Answers: map[string]systemone.Answer{
			"department": {Choice: &systemone.ChoiceAnswer{
				Choice: "billing", Probabilities: map[string]float64{"billing": .9, "technical": .1},
			}},
			"urgency": {Score: &systemone.ScoreAnswer{Score: .7, Probabilities: []float64{.3, .7}}},
			"refund":  {Noul: &systemone.NoulAnswer{Probability: .98}},
		}},
	}}})
	client, err := systemone.New(systemone.Config{Backend: backend})
	if err != nil {
		fmt.Println(err)
		return
	}
	state := map[string]any{
		"ticket":  map[string]string{"message": "I was charged twice. Please refund the duplicate today."},
		"account": map[string]any{"previous_refunds": 0},
	}
	answers, err := client.Ask(context.Background(), state, department, urgency, refund)
	if err != nil {
		fmt.Println(err)
		return
	}
	dept, err := department.In(answers)
	if err != nil {
		fmt.Println(err)
		return
	}
	score, err := urgency.In(answers)
	if err != nil {
		fmt.Println(err)
		return
	}
	wantsRefund, err := refund.In(answers)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("Department:", dept.Choice)
	fmt.Println("Urgency:", score.Score)
	fmt.Println("Refund probability:", wantsRefund.Probability)
	// Output:
	// Department: billing
	// Urgency: 0.7
	// Refund probability: 0.98
}
