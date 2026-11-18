package model

import (
	"slices"
	"testing"
)

func TestWorkFor(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name           string
		createdBy, run int64
		want           []int64
	}{
		{"a person's", 3, 0, []int64{3}},
		{"a person's, run by another", 3, 4, []int64{3, 4}},
		{"Dash's, given to nobody yet: no one's", 0, 0, []int64{0}},
		{"Dash's, given to a member (or queued by one)", 0, 4, []int64{4}},
		{"a deleted account's", DeletedUser, 0, []int64{DeletedUser}},
	} {
		if got := (Todo{CreatedBy: c.createdBy, RunBy: c.run}).WorkFor(); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
