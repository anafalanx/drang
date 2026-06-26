package eval

import (
	"testing"

	"github.com/anafalanx/drang/internal/value"
)

func TestErrInspection(t *testing.T) {
	e := value.MakeErr("boom", 42)
	if v := callBuiltin(t, "is_err", e); !v.Truthy() {
		t.Error("is_err(Err) should be true")
	}
	if v := callBuiltin(t, "err_code", e); v.AsInt() != 42 {
		t.Errorf("err_code(Err) = %d, want 42", v.AsInt())
	}
	if v := callBuiltin(t, "err_msg", e); v.AsStr() != "boom" {
		t.Errorf("err_msg(Err) = %q, want \"boom\"", v.AsStr())
	}

	ok := value.MakeInt(5)
	if v := callBuiltin(t, "is_err", ok); v.Truthy() {
		t.Error("is_err(non-error) should be false")
	}
	if v := callBuiltin(t, "err_code", ok); v.AsInt() != 0 {
		t.Errorf("err_code(non-error) = %d, want 0", v.AsInt())
	}
	if v := callBuiltin(t, "err_msg", ok); v.AsStr() != "" {
		t.Errorf("err_msg(non-error) = %q, want \"\"", v.AsStr())
	}
}
