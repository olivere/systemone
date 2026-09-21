package sonetest_test

import (
	"encoding/json/jsontext"
	"testing"

	"github.com/olivere/systemone"
	"github.com/olivere/systemone/sonetest"
)

func TestContentSnapshots(t *testing.T) {
	t.Parallel()
	c := systemone.MustContent(map[string]string{"focus": "primary"})
	fake := sonetest.New(sonetest.Config{Steps: []sonetest.Step{{}}})
	if !fake.Capabilities().StructuredContent {
		t.Fatal("default fake does not support content")
	}
	req := systemone.Request{State: jsontext.Value(`{}`), Questions: []systemone.QuestionSpec{{Key: "q", Kind: systemone.ScoreKind, InstructionsContent: c, LevelContents: []systemone.Content{c, c}}}}
	if _, err := fake.Evaluate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	req.Questions[0].LevelContents[0] = systemone.MustContent("changed")
	calls := fake.Calls()
	if calls[0].Questions[0].LevelContents[0] != c {
		t.Fatal("retained input slice")
	}
	calls[0].Questions[0].LevelContents[0] = systemone.MustContent("changed again")
	if fake.Calls()[0].Questions[0].LevelContents[0] != c {
		t.Fatal("returned internal slice")
	}
}
