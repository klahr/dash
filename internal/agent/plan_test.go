package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

func TestParsePlanTurn(t *testing.T) {
	got, err := ParsePlanTurn("Sure!\n```json\n{\"note\":\"hi\",\"questions\":[{\"id\":\"a\",\"text\":\"Who?\",\"default\":\"Me\"}],\"brief\":\"# X\"}\n```")
	if err != nil || got.Note != "hi" || len(got.Questions) != 1 || got.Questions[0].Default != "Me" || got.Brief != "# X" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []string{"", "no json here", "{not json}"} {
		if _, err := ParsePlanTurn(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

// The interviewer gets the idea, the brief and the latest rounds with
// their answers, and is told whether to start or go on.
func TestPlanInput(t *testing.T) {
	p := model.Plan{Idea: "A recipe box", Name: "Recipes", Topics: model.NewPlanTopics()}
	if in := PlanInput(p); !strings.HasPrefix(in, "Start the interview") || !strings.Contains(in, `"idea": "A recipe box"`) {
		t.Errorf("first:\n%s", in)
	}
	for i := range planRounds + 2 {
		p.Rounds = append(p.Rounds, model.PlanRound{At: time.Now(), Questions: []model.PlanQuestion{{ID: "q" + string(rune('a'+i)), Text: "Q?"}}})
	}
	last := &p.Rounds[len(p.Rounds)-1]
	last.Answers = []model.PlanAnswer{{Question: last.Questions[0].ID, Text: "Me", Default: true}}
	last.Message = "and fast"
	p.Brief = "# Recipes"
	in := PlanInput(p)
	for _, want := range []string{"Update the brief", `"accepted_default": true`, `"person_also_said": "and fast"`, `"current_brief": "# Recipes"`} {
		if !strings.Contains(in, want) {
			t.Errorf("lacks %q:\n%s", want, in)
		}
	}
	if strings.Contains(in, `"qa"`) || strings.Count(in, `"Q?"`) != planRounds {
		t.Errorf("sent more than the latest %d rounds:\n%s", planRounds, in)
	}
	if !strings.Contains(PlanSystem, "users, features, data, stack, auth, deploy, scope") {
		t.Error("the system prompt doesn't name the topic ids")
	}
}
