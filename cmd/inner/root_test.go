package main

import (
	"reflect"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"run -p claude", []string{"run", "-p", "claude"}},
		{"run   -p   claude", []string{"run", "-p", "claude"}},
		{`run --arg "fix the bug"`, []string{"run", "--arg", "fix the bug"}},
		{`run --arg 'fix the bug'`, []string{"run", "--arg", "fix the bug"}},
		{`echo "a b" c`, []string{"echo", "a b", "c"}},
		{`a\ b`, []string{"a b"}},
		{`--msg "he said \"hi\""`, []string{"--msg", `he said "hi"`}},
		{`'single '"double"`, []string{"single double"}},
	}
	for _, c := range cases {
		got := splitArgs(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitArgs(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}
