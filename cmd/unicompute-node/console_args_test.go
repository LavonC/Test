package main

import (
	"reflect"
	"testing"
)

func TestSplitConsoleArgs(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{
			name: "empty string",
			line: "",
			want: nil,
		},
		{
			name: "whitespace only",
			line: "   \t\r\n  ",
			want: nil,
		},
		{
			name: "simple tokens",
			line: "run node-B python hello.py",
			want: []string{"run", "node-B", "python", "hello.py"},
		},
		{
			name: "double quoted path with spaces",
			line: `run node-B python "./my examples/hello world.py" ./input.txt`,
			want: []string{"run", "node-B", "python", "./my examples/hello world.py", "./input.txt"},
		},
		{
			name: "single quoted path with spaces",
			line: `run node-B cpp './my examples/fib calc.cpp'`,
			want: []string{"run", "node-B", "cpp", "./my examples/fib calc.cpp"},
		},
		{
			name: "windows path unquoted with backslashes",
			line: `run node-B python C:\Users\name\file.py`,
			want: []string{"run", "node-B", "python", `C:\Users\name\file.py`},
		},
		{
			name: "windows path double quoted with spaces and backslashes",
			line: `run node-B python "C:\Users\my name\project dir\file.py"`,
			want: []string{"run", "node-B", "python", `C:\Users\my name\project dir\file.py`},
		},
		{
			name: "escaped matching quotes inside quotes",
			line: `run node-B python "C:\Users\name\say \"hi\".py" 'it\'s.txt'`,
			want: []string{"run", "node-B", "python", `C:\Users\name\say "hi".py`, `it's.txt`},
		},
		{
			name: "empty quoted argument preserved",
			line: `cmd "" '' arg2`,
			want: []string{"cmd", "", "", "arg2"},
		},
		{
			name: "mixed single and double quotes inside each other",
			line: `echo "hello 'world'" 'foo "bar"'`,
			want: []string{"echo", "hello 'world'", `foo "bar"`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitConsoleArgs(tc.line)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("splitConsoleArgs(%q) = %#v, want %#v", tc.line, got, tc.want)
			}
		})
	}
}
