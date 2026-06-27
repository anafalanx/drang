package main

import (
	"reflect"
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
