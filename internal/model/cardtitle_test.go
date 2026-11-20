package model

import (
	"strings"
	"testing"
)

func TestCleanCardTitleFromText(t *testing.T) {
	long := strings.Repeat("word ", 30)
	for _, tc := range []struct{ desc, title, rest string }{
		{"Waitlist when a clinic is fully booked\nPatients join a list.", "Waitlist when a clinic is fully booked", "Patients join a list."},
		{"\n\n# Waitlist\n\nPatients join.\nThen wait.", "Waitlist", "Patients join.\nThen wait."},
		{"Just a title", "Just a title", ""},
		// A first line too long for the title stays in the description.
		{long + "\nMore.", firstLine(long, 80), strings.TrimSpace(long) + " \nMore."},
	} {
		c := Card{Description: tc.desc}
		if err := CleanCard(&c); err != nil {
			t.Fatal(err)
		}
		if c.Title != tc.title || c.Description != tc.rest {
			t.Errorf("%q: title %q, description %q; want %q, %q", tc.desc, c.Title, c.Description, tc.title, tc.rest)
		}
	}
	// A title given leaves the description whole.
	c := Card{Title: "Waitlist", Description: "Waitlist\nPatients join."}
	if err := CleanCard(&c); err != nil || c.Description != "Waitlist\nPatients join." {
		t.Errorf("given title: description %q, %v", c.Description, err)
	}
}
