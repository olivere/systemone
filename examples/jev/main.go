// Command jev evaluates a support ticket against the real TypeSafe API.
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
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	text := flag.String("text", "I was charged twice. Please refund the duplicate.", "ticket to evaluate")
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
	department := systemone.MustChoice(
		"department", "Which team should handle this ticket?",
		systemone.Opt(Billing, "Payments, invoicing, and refunds"),
		systemone.Opt(Technical, "Bugs, outages, and integrations"),
	)
	urgency := systemone.MustScore(
		"urgency", "How soon does this ticket need attention?",
		"Can wait until next week", "Needs attention today", "Blocked and needs attention now",
	)
	wantsRefund := systemone.MustNoul("refund", "Does the customer request a refund?")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	answers, err := client.Ask(ctx, *text, department, urgency, wantsRefund)
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
	refund, err := wantsRefund.In(answers)
	if err != nil {
		return err
	}
	_, err = fmt.Printf("Model: %s\nDepartment: %s\nDepartment probabilities: %v\nUrgency (0–2): %.3f\nRefund probability: %.3f\nTokens: %d input, %d output\n",
		answers.Model(), dept.Choice, dept.Probabilities, score.Score, refund.Probability,
		answers.Usage().InputTokens, answers.Usage().OutputTokens)
	return err
}
