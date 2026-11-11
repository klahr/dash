package main

import (
	"encoding/json"
	"fmt"

	"github.com/klahr/dash/internal/tracker"
	"github.com/klahr/dash/internal/tracker/trackertest"
)

// testTracker is the fake board backend dash's own end-to-end tests start a
// Dash with (DASH_TEST_TRACKER, as JSON): an in-memory tracker
// (internal/tracker/trackertest) registered as Name, with these boards.
type testTracker struct {
	Name   string `json:"name"`
	Boards []struct {
		Name    string   `json:"name"`
		Token   string   `json:"token"`
		Columns []string `json:"columns"`
		Cards   []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Column      string `json:"column"`
		} `json:"cards"`
	} `json:"boards"`
}

// forever is a test's Cleanup for a backend registered for the process's life.
type forever struct{}

func (forever) Cleanup(func()) {}

// registerTestTracker registers the fake board backend seed describes.
func registerTestTracker(seed string) error {
	var tt testTracker
	if err := json.Unmarshal([]byte(seed), &tt); err != nil {
		return fmt.Errorf("DASH_TEST_TRACKER: %w", err)
	}
	if tt.Name == "" {
		tt.Name = "fake"
	}
	p := trackertest.Register(forever{}, tt.Name)
	for _, b := range tt.Boards {
		p.AddBoard(b.Name, b.Token, b.Columns...)
		for _, c := range b.Cards {
			p.AddCard(b.Name, tracker.Item{Title: c.Title, Description: c.Description, Column: c.Column})
		}
	}
	return nil
}
