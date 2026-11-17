package agent

import (
	"reflect"
	"testing"
)

func TestParseGroups(t *testing.T) {
	out := "Here you go:\n```json\n" + `[
  {"title": "\"Check errors in the store.\"", "issues": [1, 3]},
  {"title": "", "issues": [2, 0, -4]},
  {"title": "Nothing", "issues": []}
]` + "\n```"
	got, err := parseGroups(out)
	want := []IssueGroup{{Title: "Check errors in the store", Issues: []int{0, 2}}, {Issues: []int{1}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("parseGroups: %+v, %v\nwant %+v", got, err, want)
	}
	for _, bad := range []string{"", "no idea", "[]", `[{"title":"x","issues":[]}]`, "[{"} {
		if _, err := parseGroups(bad); err == nil {
			t.Errorf("parseGroups(%q): no error", bad)
		}
	}
}
