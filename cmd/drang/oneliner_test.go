package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestExpandOneLinerCluster(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"ne", []string{"-ne", "CODE"}, []string{"-n", "-e", "CODE"}},
		{"ane with file", []string{"-ane", "CODE", "f"}, []string{"-a", "-n", "-e", "CODE", "f"}},
		{"np no e", []string{"-np", "f"}, []string{"-n", "-p", "f"}},
		{"single -n untouched", []string{"-n", "x"}, []string{"-n", "x"}},
		{"bare -e untouched", []string{"-e", "CODE"}, []string{"-e", "CODE"}},
		{"long flag untouched", []string{"--ast", "x"}, []string{"--ast", "x"}},
		{"unknown letter is not a cluster", []string{"-nx", "f"}, []string{"-nx", "f"}},
		{"e not last is not expanded", []string{"-en", "CODE"}, []string{"-en", "CODE"}},
		{"ea is not expanded", []string{"-ea", "CODE"}, []string{"-ea", "CODE"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := expandOneLinerCluster(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("expandOneLinerCluster(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestParseCLIOptions(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
		check   func(t *testing.T, got cliOptions)
	}{
		{
			name: "terminator permits dash path",
			args: []string{"--", "-script.dr", "-x"},
			check: func(t *testing.T, got cliOptions) {
				if !got.programForced || !reflect.DeepEqual(got.rest, []string{"-script.dr", "-x"}) {
					t.Fatalf("rest = %#v", got.rest)
				}
			},
		},
		{
			name: "terminator forces literal dash e path",
			args: []string{"--", "-e", "arg"},
			check: func(t *testing.T, got cliOptions) {
				if !got.programForced || inlineSourceSelected(got) || !reflect.DeepEqual(got.rest, []string{"-e", "arg"}) {
					t.Fatalf("got %#v", got)
				}
			},
		},
		{
			name: "inline source remains unforced",
			args: []string{"-e", "say(1)", "arg"},
			check: func(t *testing.T, got cliOptions) {
				if got.programForced || !inlineSourceSelected(got) || !reflect.DeepEqual(got.rest, []string{"-e", "say(1)", "arg"}) {
					t.Fatalf("got %#v", got)
				}
			},
		},
		{
			name: "bare terminator keeps no program behavior",
			args: []string{"--"},
			check: func(t *testing.T, got cliOptions) {
				if !got.programForced || len(got.rest) != 0 {
					t.Fatalf("got %#v", got)
				}
			},
		},
		{name: "unknown leading flag", args: []string{"--wat", "x.dr"}, wantErr: "unknown option"},
		{name: "diagnostic stream conflict", args: []string{"--ast", "-n", "x.dr"}, wantErr: "cannot be combined"},
		{name: "autosplit needs stream", args: []string{"-a", "x.dr"}, wantErr: "requires -n or -p"},
		{name: "in-place needs print", args: []string{"-i.bak", "x.dr"}, wantErr: "requires -p"},
		{name: "duplicate option", args: []string{"-n", "-n", "x.dr"}, wantErr: "more than once"},
		{name: "mode conflict", args: []string{"--tokens", "--ast", "x.dr"}, wantErr: "mutually exclusive"},
		{name: "help does not hide bad combination", args: []string{"-n", "--help"}, wantErr: "used by itself"},
		{
			name: "program args remain untouched",
			args: []string{"-p", "-e", "$_", "--wat"},
			check: func(t *testing.T, got cliOptions) {
				if !got.streamP || !reflect.DeepEqual(got.rest, []string{"-e", "$_", "--wat"}) {
					t.Fatalf("got %#v", got)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCLIOptions(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}
