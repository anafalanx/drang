package eval

import "testing"

// The three-sigil grace window: .foo user functions work alongside the legacy bare
// form, are disjoint from bare builtins, and compose with field access. (Migration
// of the corpus to .foo, then removal of the bare-user form, comes later.)
func TestUserFnSigil(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"define-call", `fn .clean($x) { upper($x) }  say(.clean("hi"))`, "HI\n"},
		{"first-class-to-hof", `fn .dbl($x) { $x * 2 }  say(to_json(map([1,2,3], .dbl)))`, "[2,4,6]\n"},
		{"recursion", `fn .fact($n) { if $n <= 1 { return 1 }  $n * .fact($n - 1) }  say(.fact(5))`, "120\n"},
		{"coexists-with-bare", `fn foo() { "bare" }  fn .foo() { "dot" }  say(foo() ~ .foo())`, "baredot\n"},
		{"call-then-field-access", `fn .mk() { {n: 7} }  say(.mk().n)`, "7\n"},
		// a user .len is disjoint from the len builtin — no shadow, no collision
		{"disjoint-from-builtin", `fn .len($x) { 99 }  say("${.len([1,2])}/${len([1,2])}")`, "99/2\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(t, c.src); got != c.want {
				t.Errorf("%s\n got %q\nwant %q", c.src, got, c.want)
			}
		})
	}
}
