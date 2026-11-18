package model

import (
	"slices"
	"strings"
	"testing"
)

func TestCleanTags(t *testing.T) {
	got, err := CleanTags([]string{"  big   win ", "Big Win", "", "ux", "UX", "q3"})
	if err != nil || !slices.Equal(got, []string{"big win", "ux", "q3"}) {
		t.Errorf("CleanTags = %q, %v", got, err)
	}
	if got, err := CleanTags(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no tags: %#v, %v", got, err)
	}
	if _, err := CleanTags([]string{strings.Repeat("x", MaxCardTag+1)}); err == nil {
		t.Error("a tag that is too long is taken")
	}
	many := []string{}
	for i := range MaxCardTags + 1 {
		many = append(many, strings.Repeat("t", i+1))
	}
	if _, err := CleanTags(many); err == nil {
		t.Error("too many tags are taken")
	}
}
