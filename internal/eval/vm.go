package eval

import (
	"fmt"
	"sync"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/token"
	"github.com/anafalanx/drang/internal/value"
)

// regFrame wraps a register backing array (and a for-in iterator slice) so they
// can be pooled. A function call borrows one for the duration of vmRun and returns
// it; the frame never escapes (the return value is copied out, closures capture the
// env not the registers), so reuse is safe. The pool is goroutine-safe, which
// matters because pmap/spawn run the VM in worker goroutines — each goroutine
// borrows its own frames.
type regFrame struct {
	regs  []value.Value
	iters []*forIter
}

var regPool = sync.Pool{New: func() any { return new(regFrame) }}

// forIter is a for-in iterator. It snapshots arrays/maps/strings (so a body that
// mutates the collection can't disturb the iteration, matching the walker) but
// stays lazy over a range — a 0..1_000_000 loop allocates nothing per element.
type forIter struct {
	firsts, seconds []value.Value // snapshot mode (Arr/Map/Str): index/key and value
	pos             int
	isRange         bool
	cur, hi, idx    int64 // range mode
	rangeDone       bool
}

// newForIter builds an iterator over it, or an error if it isn't iterable.
func newForIter(it value.Value) (*forIter, error) {
	switch it.Tag() {
	case value.Arr:
		elems := it.Obj().(*value.Array).Elems
		seconds := append([]value.Value(nil), elems...) // snapshot
		firsts := make([]value.Value, len(elems))
		for i := range elems {
			firsts[i] = value.MakeInt(int64(i))
		}
		return &forIter{firsts: firsts, seconds: seconds}, nil
	case value.Map:
		m := it.Obj().(*value.OrderedMap)
		return &forIter{
			firsts:  append([]value.Value(nil), m.Keys()...),
			seconds: append([]value.Value(nil), m.Vals()...),
		}, nil
	case value.Str:
		var firsts, seconds []value.Value
		idx := int64(0)
		for _, rn := range it.AsStr() { // by rune
			firsts = append(firsts, value.MakeInt(idx))
			seconds = append(seconds, value.MakeStr(string(rn)))
			idx++
		}
		return &forIter{firsts: firsts, seconds: seconds}, nil
	case value.Range:
		r := it.Obj().(*value.IntRange)
		return &forIter{isRange: true, cur: r.Lo, hi: r.Hi, rangeDone: r.Hi < r.Lo}, nil
	default:
		return nil, fmt.Errorf("cannot iterate over a %s", it.TypeName())
	}
}

// next returns (first, second, ok): first is the index/key, second the value.
func (it *forIter) next() (value.Value, value.Value, bool) {
	if it.isRange {
		if it.rangeDone {
			return value.Value{}, value.Value{}, false
		}
		first, second := value.MakeInt(it.idx), value.MakeInt(it.cur)
		if it.cur == it.hi { // stop after binding hi, so a max-int64 bound can't overflow
			it.rangeDone = true
		} else {
			it.cur++
			it.idx++
		}
		return first, second, true
	}
	if it.pos >= len(it.seconds) {
		return value.Value{}, value.Value{}, false
	}
	first, second := it.firsts[it.pos], it.seconds[it.pos]
	it.pos++
	return first, second, true
}

// RunProgramVM is the bytecode-backend entry point: it compiles the program and
// runs it on the register VM, falling back to the tree-walker for any program the
// compiler does not yet cover. The two backends share value.Value, the Env, every
// builtin, and the call seam (resolveAndCall), so a half-compiled program stays
// coherent and produces byte-identical results — the property the parity tests
// assert across the whole suite.
func RunProgramVM(prog *ast.Program, env *Env) error {
	p, ok := compileProgram(prog)
	if !ok {
		return RunProgram(prog, env)
	}
	_, err := vmRun(p, env, nil, 0)
	return err
}

// vmCallFunction runs a compiled function. A register-mode function needs no
// per-call env — its params and locals are registers, so it runs directly in the
// captured env with the arguments preloaded into the low registers. An Env-mode
// function binds its params in a fresh child env, exactly like the walker's local
// scope. The arity check happens in the shared callFunction before dispatch.
func vmCallFunction(fn *Function, args []value.Value, bodyDepth int) (value.Value, error) {
	var v value.Value
	var err error
	if fn.Proto.RegLocals {
		// A register-mode function keeps its locals in registers and runs directly in its
		// (shared) capture env — no per-call env allocation. The recursion depth rides
		// through vmRun as a plain int, so nothing shared is mutated.
		v, err = vmRun(fn.Proto, fn.Env, args, bodyDepth)
	} else {
		local := fn.Env.child()
		for i, p := range fn.Params {
			_ = local.define(p, args[i], false)
		}
		v, err = vmRun(fn.Proto, local, nil, bodyDepth)
	}
	// A ? that propagated an error out of the body becomes this function's error
	// RESULT, matching the walker's errSignal handling in callFunction.
	if es, ok := err.(errSignal); ok {
		return es.e, nil
	}
	return v, err
}

// posAt returns the source position (line, col) of the instruction at ip, or (0,0) if ip is
// out of range or the instruction carries no position.
func (p *Proto) posAt(ip int) (line, col int) {
	if ip >= 0 && ip < len(p.Positions) {
		return p.Positions[ip].Line, p.Positions[ip].Col
	}
	return 0, 0
}

// vmRun executes a Proto against env. Named variables resolve through env (v1,
// Env-backed); registers hold expression temporaries. The env pointer moves as
// OpPushScope/OpPopScope enter and leave block scopes, mirroring the walker's
// env.child() discipline exactly (a fresh child per if/else body and per loop
// iteration). depth is this body's user-call depth; every call it makes is passed
// depth so the recursion guard tracks the Go-stack depth without touching env.
func vmRun(p *Proto, env *Env, params []value.Value, depth int) (res value.Value, rerr error) {
	fr := regPool.Get().(*regFrame)
	if cap(fr.regs) < p.NumRegs {
		fr.regs = make([]value.Value, p.NumRegs)
	}
	defer regPool.Put(fr)
	regs := fr.regs[:p.NumRegs]
	n := copy(regs, params) // register mode: params preloaded into low slots
	for i := n; i < len(regs); i++ {
		regs[i] = value.MakeNil() // also clears any stale values from prior use
	}
	var iters []*forIter
	if p.NumIters > 0 {
		if cap(fr.iters) < p.NumIters {
			fr.iters = make([]*forIter, p.NumIters)
		}
		iters = fr.iters[:p.NumIters]
		for i := range iters {
			iters[i] = nil // clear stale iterators from prior use
		}
	}
	code := p.Code
	ip := 0
	// On an aborting error, tag it with the failing instruction's source position
	// (ip-1, since ip is advanced before dispatch). Control-flow signals and
	// already-positioned errors pass through untouched.
	defer func() {
		if rerr == nil {
			return
		}
		switch rerr.(type) {
		case errSignal, returnSignal, exitSignal, *posError:
			return
		}
		if idx := ip - 1; idx >= 0 && idx < len(p.Positions) {
			if ps := p.Positions[idx]; ps.Line != 0 {
				rerr = &posError{line: ps.Line, col: ps.Col, msg: rerr.Error()}
			}
		}
	}()
	for ip < len(code) {
		in := code[ip]
		ip++
		switch in.Op {
		case OpLoadConst:
			regs[in.A] = p.Consts[in.B]
		case OpLoadNil:
			regs[in.A] = value.MakeNil()
		case OpMove:
			regs[in.A] = regs[in.B]
		case OpAdd:
			v, err := arith(token.PLUS, regs[in.B], regs[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpSub:
			v, err := arith(token.MINUS, regs[in.B], regs[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpMul:
			v, err := arith(token.STAR, regs[in.B], regs[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpDiv:
			v, err := arith(token.SLASH, regs[in.B], regs[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpMod:
			v, err := arith(token.PERCENT, regs[in.B], regs[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpConcat:
			regs[in.A] = value.MakeStr(regs[in.B].Display() + regs[in.C].Display())
		case OpAddK:
			v, err := arith(token.PLUS, regs[in.B], p.Consts[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpSubK:
			v, err := arith(token.MINUS, regs[in.B], p.Consts[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpMulK:
			v, err := arith(token.STAR, regs[in.B], p.Consts[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpDivK:
			v, err := arith(token.SLASH, regs[in.B], p.Consts[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpModK:
			v, err := arith(token.PERCENT, regs[in.B], p.Consts[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpConcatK:
			regs[in.A] = value.MakeStr(regs[in.B].Display() + p.Consts[in.C].Display())
		case OpEq:
			regs[in.A] = value.MakeBool(equal(regs[in.B], regs[in.C]))
		case OpNe:
			regs[in.A] = value.MakeBool(!equal(regs[in.B], regs[in.C]))
		case OpLt:
			v, err := compare(token.LT, regs[in.B], regs[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpLe:
			v, err := compare(token.LE, regs[in.B], regs[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpGt:
			v, err := compare(token.GT, regs[in.B], regs[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpGe:
			v, err := compare(token.GE, regs[in.B], regs[in.C])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = v
		case OpSpaceship:
			if b := regs[in.B]; b.IsErr() {
				regs[in.A] = b // errors flow as values
			} else if c := regs[in.C]; c.IsErr() {
				regs[in.A] = c
			} else {
				cmp, err := threeway(regs[in.B], regs[in.C])
				if err != nil {
					return value.MakeNil(), err
				}
				regs[in.A] = value.MakeInt(int64(cmp))
			}
		case OpNeg:
			x := regs[in.B]
			switch x.Tag() {
			case value.Int:
				regs[in.A] = value.MakeInt(-x.AsInt())
			case value.Float:
				regs[in.A] = value.MakeFloat(-x.AsFloat())
			case value.Err:
				regs[in.A] = x // errors flow as values
			default:
				return value.MakeNil(), fmt.Errorf("cannot negate %s", x.TypeName())
			}
		case OpNot:
			regs[in.A] = value.MakeBool(!regs[in.B].Truthy())
		case OpMakeArray:
			base, count := in.B, in.C
			elems := make([]value.Value, count)
			copy(elems, regs[base:base+count]) // copy out: the register frame is reused
			regs[in.A] = value.MakeArray(elems)
		case OpMakeMap:
			base, count := in.B, in.C
			m := value.MakeMap()
			om := m.Obj().(*value.OrderedMap)
			result := m
			for i := int32(0); i < count; i++ {
				k := regs[base+2*i]
				if !value.Hashable(k) {
					result = value.MakeErr("unhashable map key: "+k.TypeName(), 1)
					break
				}
				om.Set(k, regs[base+2*i+1])
			}
			regs[in.A] = result
		case OpJumpUnhashable:
			// Per-key hashability check for a dynamic map-literal key, emitted right after the
			// key is evaluated and before its value — so a bad key fails BEFORE that value and
			// any later entries run, matching the tree-walker exactly (byte-identical parity).
			if k := regs[in.B]; !value.Hashable(k) {
				regs[in.A] = value.MakeErr("unhashable map key: "+k.TypeName(), 1)
				ip = int(in.C)
			}
		case OpMakeRange:
			lo, hi := regs[in.B], regs[in.C]
			if lo.Tag() != value.Int || hi.Tag() != value.Int {
				regs[in.A] = value.MakeErr(fmt.Sprintf("range bounds must be ints, got %s..%s", lo.TypeName(), hi.TypeName()), 1)
			} else {
				regs[in.A] = value.MakeRange(lo.AsInt(), hi.AsInt())
			}
		case OpMakeRegex:
			regs[in.A] = makeRegex(p.Consts[in.B].AsStr()) // cached; bad pattern -> Err
		case OpIndex:
			regs[in.A] = indexRead(regs[in.B], regs[in.C])
		case OpField:
			regs[in.A] = fieldRead(regs[in.B], p.Consts[in.C].AsStr())
		case OpResolveLocalContainer:
			cont, created, err := containerForWrite(regs[in.A], kindFor(regs[in.B]), p.Consts[in.C].AsStr())
			if err != nil {
				return value.MakeNil(), err
			}
			if created {
				regs[in.A] = cont // autoviv writes back to the local slot
			}
		case OpResolveVarContainer:
			name := p.Consts[in.B].AsStr()
			cur, _ := env.get(name)
			cont, created, err := containerForWrite(cur, kindFor(regs[in.C]), name)
			if err != nil {
				return value.MakeNil(), err
			}
			if created {
				set, frozen := env.set(name, cont)
				if !set {
					if frozen {
						return value.MakeNil(), fmt.Errorf("cannot assign to constant $%s", name)
					}
					return value.MakeNil(), fmt.Errorf("undefined variable $%s (declare it with ':=' first)", name)
				}
			}
			regs[in.A] = cont
		case OpAssignSlot:
			nv, err := assignSlot(regs[in.A], regs[in.B], token.Kind(in.C), regs[in.B+1])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.B+1] = nv
		case OpResolveSlot:
			cont, err := resolveSlot(regs[in.C], regs[in.A], kindFor(regs[in.B]))
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = cont
		case OpCompoundLocal:
			nv, err := compound(token.Kind(in.C), regs[in.A], regs[in.B])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = nv
		case OpCompoundLocalK:
			nv, err := compound(token.Kind(in.C), regs[in.A], p.Consts[in.B])
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = nv
		case OpCompoundVar:
			name := p.Consts[in.A].AsStr()
			cur, _ := env.get(name)
			nv, err := compound(token.Kind(in.C), cur, regs[in.B])
			if err != nil {
				return value.MakeNil(), err
			}
			set, frozen := env.set(name, nv)
			if !set {
				if frozen {
					return value.MakeNil(), fmt.Errorf("cannot assign to constant $%s", name)
				}
				return value.MakeNil(), fmt.Errorf("undefined variable $%s (declare it with ':=' first)", name)
			}
			regs[in.B] = nv
		case OpGetVar:
			name := p.Consts[in.B].AsStr()
			v, ok := env.get(name)
			if !ok {
				return value.MakeNil(), fmt.Errorf("undefined variable $%s", name)
			}
			regs[in.A] = v
		case OpDeclVar:
			name := p.Consts[in.B].AsStr()
			if err := env.define(name, regs[in.A], in.C != 0); err != nil {
				return value.MakeNil(), err
			}
		case OpSetVar:
			name := p.Consts[in.B].AsStr()
			set, frozen := env.set(name, regs[in.A])
			if !set {
				if frozen {
					return value.MakeNil(), fmt.Errorf("cannot assign to constant $%s", name)
				}
				return value.MakeNil(), fmt.Errorf("undefined variable $%s (declare it with ':=' first)", name)
			}
		case OpJump:
			ip = int(in.B)
		case OpJumpIfFalsy:
			if !regs[in.A].Truthy() {
				ip = int(in.B)
			}
		case OpJumpIfTruthy:
			if regs[in.A].Truthy() {
				ip = int(in.B)
			}
		case OpJumpIfDefined:
			if v := regs[in.A]; v.Tag() != value.Nil && !v.IsErr() {
				ip = int(in.B)
			}
		case OpJmpFalseLt:
			res, err := compare(token.LT, regs[in.A], regs[in.B])
			if err != nil {
				return value.MakeNil(), err
			}
			if !res.Truthy() {
				ip = int(in.C)
			}
		case OpJmpFalseLe:
			res, err := compare(token.LE, regs[in.A], regs[in.B])
			if err != nil {
				return value.MakeNil(), err
			}
			if !res.Truthy() {
				ip = int(in.C)
			}
		case OpJmpFalseGt:
			res, err := compare(token.GT, regs[in.A], regs[in.B])
			if err != nil {
				return value.MakeNil(), err
			}
			if !res.Truthy() {
				ip = int(in.C)
			}
		case OpJmpFalseGe:
			res, err := compare(token.GE, regs[in.A], regs[in.B])
			if err != nil {
				return value.MakeNil(), err
			}
			if !res.Truthy() {
				ip = int(in.C)
			}
		case OpJmpFalseEq:
			if !equal(regs[in.A], regs[in.B]) {
				ip = int(in.C)
			}
		case OpJmpFalseNe:
			if equal(regs[in.A], regs[in.B]) {
				ip = int(in.C)
			}
		case OpPushScope:
			env = env.child()
		case OpPopScope:
			env = env.parent
		case OpCall:
			base, argc := in.A, in.B
			name := p.Consts[in.C].AsStr()
			res, err := resolveAndCall(name, regs[base:base+argc], env, depth)
			if err != nil {
				return value.MakeNil(), err
			}
			regs[base] = res
		case OpCallBuiltin:
			base, argc := in.A, in.B
			res, err := dispatchNonUser(p.Consts[in.C].AsStr(), regs[base:base+argc], env, depth)
			if err != nil {
				return value.MakeNil(), err
			}
			regs[base] = res
		case OpCallValue:
			callee := regs[in.C]
			fn, ok := asFunction(callee)
			if !ok {
				return value.MakeNil(), fmt.Errorf("cannot call a %s", callee.TypeName())
			}
			res, err := callFunction(fn, regs[in.A:in.A+in.B], depth)
			if err != nil {
				return value.MakeNil(), err
			}
			regs[in.A] = res
		case OpGetIdent:
			name := p.Consts[in.B].AsStr()
			v, ok := env.get(name)
			if !ok {
				b, found := builtins[name]
				if !found {
					return value.MakeNil(), fmt.Errorf("undefined: %s%s", name, loopKeywordHint(name))
				}
				// a bare builtin name is a first-class function value (mirrors the walker)
				v = value.MakeObj(value.Func, &Function{Name: name, Builtin: b})
			}
			regs[in.A] = v
		case OpMakeClosure:
			t := p.Protos[in.B]
			regs[in.A] = value.MakeObj(value.Func, &Function{
				Name: t.Name, Params: t.Params, Defaults: t.Defaults, Body: t.Body, Env: env, Proto: t.Proto,
			})
		case OpIterNew:
			if src := regs[in.B]; src.IsErr() {
				// Match the walker: an unhandled Err iterable propagates (message preserved).
				l, c := p.posAt(ip - 1)
				return value.MakeNil(), errSignal{e: src, line: l, col: c}
			}
			it, err := newForIter(regs[in.B])
			if err != nil {
				return value.MakeNil(), err
			}
			iters[in.A] = it
		case OpIterNext1:
			_, second, ok := iters[in.A].next()
			if !ok {
				ip = int(in.C)
			} else {
				regs[in.B] = second
			}
		case OpIterNext2:
			first, second, ok := iters[in.A].next()
			if !ok {
				ip = int(in.C)
			} else {
				regs[in.B] = first
				regs[in.B+1] = second
			}
		case OpPropagate:
			// ?: an error unwinds to the call boundary (vmCallFunction turns it into
			// the function's result; RunProgramVM lets it abort the program). A
			// non-error value simply passes through in regs[A].
			if regs[in.A].IsErr() {
				l, c := p.posAt(ip - 1)
				return value.MakeNil(), errSignal{e: regs[in.A], line: l, col: c}
			}
		case OpReturn:
			return regs[in.A], nil
		default:
			return value.MakeNil(), fmt.Errorf("vm: unknown opcode %d", in.Op)
		}
	}
	return value.MakeNil(), nil
}
