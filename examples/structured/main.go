// Command structured evaluates structured questions against the real TypeSafe API.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/olivere/systemone"
	"github.com/olivere/systemone/jev"
)

type Team string

const (
	Billing   Team = "billing"
	Technical Team = "technical"
	Other     Team = "other"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	text := flag.String("text", "I was charged twice. Please refund the duplicate today.", "ticket to evaluate")
	focus := flag.String("focus", "Classify the customer's current request, not the account history.", "routing instructions")
	model := flag.String("model", "jev-latest", "TypeSafe model or alias")
	debug := flag.Bool("debug", false, "dump HTTP requests/responses to stderr (bodies may contain sensitive data)")
	flag.Parse()

	config := jev.Config{APIKey: os.Getenv("TYPESAFE_API_KEY"), Model: *model}
	if *debug {
		config.Debug = os.Stderr
	}
	backend, err := jev.New(config)
	if err != nil {
		return err
	}
	client, err := systemone.New(systemone.Config{Backend: backend})
	if err != nil {
		return err
	}
	// Runtime content uses error-returning constructors; MustContent is for
	// definitions whose values are fixed in the program.
	instructions, err := systemone.NewContent(map[string]string{
		"question": "Which team should handle `ticket.message`?",
		"focus":    *focus,
	})
	if err != nil {
		return err
	}
	department, err := systemone.NewChoiceContent(
		"department", instructions,
		systemone.OptContent(Billing, systemone.MustContent(map[string]any{
			"description": "Payments, invoicing, and refunds",
			"examples":    []string{"Duplicate charge", "Refund request", "Incorrect invoice"},
			"excludes":    "Integration failures without a billing request",
		})),
		systemone.OptContent(Technical, systemone.MustContent([]string{
			"Bugs, outages, and integrations",
			"The customer needs help restoring product functionality.",
		})),
		systemone.Opt(Other, "Requests outside billing and technical support"),
	)
	if err != nil {
		return err
	}
	urgency := systemone.MustScoreContent(
		"urgency",
		systemone.MustContent(map[string]string{
			"question": "How soon does `ticket.message` need attention?",
			"focus":    "Use the customer's stated deadline and business impact.",
		}),
		systemone.MustContent(map[string]string{
			"description": "The request can wait until next week.",
			"example":     "A routine question with no deadline or disruption",
		}),
		systemone.MustContent(map[string]string{
			"description": "The request needs attention today but work can continue.",
			"example":     "A payment question with a deadline today",
		}),
		systemone.MustContent(map[string]string{
			"description": "The request needs immediate attention because work is blocked.",
			"example":     "An active outage stopping the customer's business",
		}),
	)
	refund, err := systemone.MustNoulContent(
		"refund",
		systemone.MustContent([]string{
			"Does the customer request a refund in `ticket.message`?",
			"Ignore previous refunds in `account`.",
		}),
	).WhenContent(
		systemone.MustContent(map[string]any{
			"description": "The customer asks to return a payment.",
			"examples":    []string{"Please refund me", "Return the duplicate charge"},
		}),
		systemone.MustContent(map[string]string{
			"description": "The customer does not ask to return a payment.",
		}),
	)
	if err != nil {
		return err
	}
	state := map[string]any{
		"ticket":  map[string]string{"message": *text},
		"account": map[string]any{"previous_refunds": 0},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	answers, err := client.Ask(ctx, state, department, urgency, refund)
	if err != nil {
		return err
	}
	dept, err := department.In(answers)
	if err != nil {
		return err
	}
	score, err := urgency.In(answers)
	if err != nil {
		return err
	}
	wantsRefund, err := refund.In(answers)
	if err != nil {
		return err
	}
	_, err = fmt.Printf("Model: %s\nDepartment: %s\nDepartment probabilities: %v\nUrgency (0–2): %.3f\nUrgency probabilities: %v\nRefund probability: %.3f\nTokens: %d input, %d output\n",
		answers.Model(), dept.Choice, dept.Probabilities, score.Score, score.Probabilities, wantsRefund.Probability,
		answers.Usage().InputTokens, answers.Usage().OutputTokens)
	return err
}
