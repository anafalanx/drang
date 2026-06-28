package eval

import (
	"strconv"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/token"
	"github.com/anafalanx/drang/internal/value"
)

// compiler lowers an AST to a Proto. It is a single recursive pass: compileExpr
// leaves a value in a destination register; compileStmt emits a statement's
// effects and (when resultReg >= 0) leaves the statement's value there, so a
// function body can return its last statement's value.
//
// Two modes:
//   - Env mode (regMode=false): named variables live in the Env (OpGetVar/OpSetVar/
//     OpDeclVar, with OpPushScope/OpPopScope per block). Used for the top-level
//     program (its vars are globals) and for functions with captured locals.
//   - Register mode (regMode=true): params and locals live in registers, resolved
//     at compile time; only free variables (globals/enclosing) hit the Env. No
//     per-call or per-block Env scope. Used for register-eligible functions.
//
// Registers are a stack with a high-water mark. Slots [0, localTop) are pinned
// locals (params, the result slot, and declared locals); temporaries are reserved
// above localTop and released after each statement. Local slots are not reclaimed
// on block exit (monotonic) — only their names go out of scope.
type compiler struct {
	code   []Instr
	consts []value.Value
	protos []*FuncTemplate
	ckeys  map[string]int32 // intern table, dedups the constant pool
	top    int32            // next free register (for temporaries)

	regMode     bool
	localScopes []map[string]int32 // name -> slot, innermost last (regMode)
	localTop    int32              // pinned register watermark (regMode)
	maxreg      int32              // high-water mark -> Proto.NumRegs
	numIters    int32              // for-in iterator slots allocated
	shadowed    map[string]bool    // names the program binds (nil = assume all; no direct dispatch)
	positions   []ast.Pos          // source position per instruction (parallel to code)
	curPos      ast.Pos            // position of the node currently being compiled
	loops       []*loopCtx         // enclosing loops, for break/next jump targets
	scopeDepth  int                // active OpPushScope nesting (Env mode); 0 in register mode
	ok          bool               // false once an unsupported node forces fallback
}

// loopCtx tracks one enclosing loop so break/next can target it. continueTarget
// is where `next` jumps (the while condition, or the for iterator-advance);
// breakJumps collects the OpJumps emitted by `break`, patched to the loop exit
// once it is known. scopeDepth is the OpPushScope nesting at loop entry, so
// break/next can pop the right number of Env scopes before jumping.
type loopCtx struct {
	continueTarget int32
	breakJumps     []int
	scopeDepth     int
}

// pushLoop opens a loop context; popLoop closes it, patching every break jump to
// the loop's exit. emitScopePops emits the OpPopScopes needed to unwind from the
// current scope depth back to the loop body's, before a break/next jump.
func (c *compiler) pushLoop(continueTarget int32) *loopCtx {
	lp := &loopCtx{continueTarget: continueTarget, scopeDepth: c.scopeDepth}
	c.loops = append(c.loops, lp)
	return lp
}

func (c *compiler) popLoop(exitTarget int32) {
	lp := c.loops[len(c.loops)-1]
	c.loops = c.loops[:len(c.loops)-1]
	for _, j := range lp.breakJumps {
		c.code[j].B = exitTarget
	}
}

func (c *compiler) emitScopePops(targetDepth int) {
	for d := c.scopeDepth; d > targetDepth; d-- {
		c.emit(OpPopScope, 0, 0, 0)
	}
}

// setPos updates the current source position from a node (keeping the last known
// position when a node carries none), so each emitted instruction is stamped with
// where it came from for runtime error reporting.
func (c *compiler) setPos(n ast.Node) {
	if l, ok := n.(interface{ Loc() (int, int) }); ok {
		if line, col := l.Loc(); line != 0 {
			c.curPos = ast.Pos{Line: line, Col: col}
		}
	}
}

// compileProgram compiles a top-level program (Env mode; values discarded), or
// returns ok=false if it contains a construct the VM does not compile yet.
func compileProgram(prog *ast.Program) (*Proto, bool) {
	c := &compiler{ckeys: map[string]int32{}, ok: true, shadowed: collectBoundNames(prog)}
	for _, s := range prog.Stmts {
		c.compileStmt(s, -1)
		if !c.ok {
			return nil, false
		}
	}
	// The dispatch loop yields nil when ip runs off the code; a program's value
	// is discarded, so no trailing OpReturn is needed.
	return c.proto(), true
}

// compileFunctionBody compiles a function/lambda body, choosing register mode when
// the body is register-eligible and Env mode otherwise. shadowed is the program's
// bound-name set, threaded through so nested functions can direct-dispatch builtins.
func compileFunctionBody(params []string, body *ast.Block, shadowed map[string]bool) (*Proto, bool) {
	if registerEligible(params, body) {
		return compileFunctionReg(params, body, shadowed)
	}
	return compileFunctionEnv(body, shadowed)
}

// compileFunctionEnv compiles an Env-backed function body (params bound in a child
// env by the caller; locals in the env).
func compileFunctionEnv(body *ast.Block, shadowed map[string]bool) (*Proto, bool) {
	c := &compiler{ckeys: map[string]int32{}, ok: true, shadowed: shadowed}
	res := c.reserve() // holds the implicit-return value
	c.compileBlock(body, res)
	if !c.ok {
		return nil, false
	}
	c.emit(OpReturn, res, 0, 0)
	return c.proto(), true
}

// compileFunctionReg compiles a register-eligible function: params occupy slots
// 0..nparams-1, the result slot follows, locals and temporaries above.
func compileFunctionReg(params []string, body *ast.Block, shadowed map[string]bool) (*Proto, bool) {
	c := &compiler{ckeys: map[string]int32{}, ok: true, regMode: true, shadowed: shadowed}
	c.localScopes = []map[string]int32{{}}
	for _, p := range params {
		c.declareLocal(p) // slots 0..nparams-1
	}
	res := c.reserveLocalSlot() // pinned result slot above the params
	c.compileBlock(body, res)
	if !c.ok {
		return nil, false
	}
	c.emit(OpReturn, res, 0, 0)
	p := c.proto()
	p.RegLocals = true
	return p, true
}

func (c *compiler) proto() *Proto {
	return &Proto{Code: c.code, Consts: c.consts, NumRegs: int(c.maxreg), NumIters: int(c.numIters), Protos: c.protos, Positions: c.positions}
}

func (c *compiler) fail() { c.ok = false }

func (c *compiler) emit(op Op, a, b, cc int32) int {
	c.code = append(c.code, Instr{Op: op, A: a, B: b, C: cc})
	c.positions = append(c.positions, c.curPos)
	return len(c.code) - 1
}

// reserve hands out the next free temporary register and grows the high-water mark.
func (c *compiler) reserve() int32 {
	r := c.top
	c.top++
	if c.top > c.maxreg {
		c.maxreg = c.top
	}
	return r
}

func (c *compiler) release(n int32) { c.top -= n }

// reserveLocalSlot pins the next register as a local-space slot (regMode). Callers
// must have no live temporaries (top == localTop) when calling it.
func (c *compiler) reserveLocalSlot() int32 {
	slot := c.localTop
	c.localTop++
	c.top = c.localTop
	if c.localTop > c.maxreg {
		c.maxreg = c.localTop
	}
	return slot
}

func (c *compiler) declareLocal(name string) int32 {
	slot := c.reserveLocalSlot()
	c.localScopes[len(c.localScopes)-1][name] = slot
	return slot
}

func (c *compiler) resolveLocal(name string) (int32, bool) {
	for i := len(c.localScopes) - 1; i >= 0; i-- {
		if slot, ok := c.localScopes[i][name]; ok {
			return slot, true
		}
	}
	return -1, false
}

// enterBlockScope/leaveBlockScope open and close a lexical block: a compiler-level
// name scope in register mode, or an Env child scope (OpPushScope/OpPopScope) in
// Env mode.
func (c *compiler) enterBlockScope() {
	if c.regMode {
		c.localScopes = append(c.localScopes, map[string]int32{})
	} else {
		c.emit(OpPushScope, 0, 0, 0)
		c.scopeDepth++
	}
}

func (c *compiler) leaveBlockScope() {
	if c.regMode {
		c.localScopes = c.localScopes[:len(c.localScopes)-1] // slots stay reserved (monotonic)
	} else {
		c.emit(OpPopScope, 0, 0, 0)
		c.scopeDepth--
	}
}

// resultOrTemp returns resultReg when it is a real slot, otherwise a fresh temp;
// the bool reports whether a temp was allocated (and must be released).
func (c *compiler) resultOrTemp(resultReg int32) (int32, bool) {
	if resultReg >= 0 {
		return resultReg, false
	}
	return c.reserve(), true
}

func (c *compiler) freeTemp(allocated bool) {
	if allocated {
		c.release(1)
	}
}

// ensureReg makes sure register r is counted in NumRegs (for a call's result slot,
// which can sit one past the live temporaries).
func (c *compiler) ensureReg(r int32) {
	if r+1 > c.maxreg {
		c.maxreg = r + 1
	}
}

// patchJump points a previously-emitted jump at the current end of the code.
func (c *compiler) patchJump(idx int) { c.code[idx].B = int32(len(c.code)) }

// patchBranch patches a jump emitted by compileBranchFalse, whose target field
// depends on the opcode (B for a plain jump-if-falsy, C for a fused compare-branch
// whose A and B hold the operands).
func (c *compiler) patchBranch(idx int) {
	target := int32(len(c.code))
	if op := c.code[idx].Op; op >= OpJmpFalseLt && op <= OpJmpFalseNe {
		c.code[idx].C = target
	} else {
		c.code[idx].B = target
	}
}

// compileBranchFalse emits a jump (target unset, patch with patchBranch) taken
// when cond is false. A comparison condition fuses into a single compare-branch;
// anything else materializes the value and uses OpJumpIfFalsy.
func (c *compiler) compileBranchFalse(cond ast.Expr) int {
	if b, ok := cond.(*ast.Binary); ok {
		if op, ok := branchFalseOp(b.Op); ok {
			rL, lt := c.compileOperand(b.L)
			rR, rt := c.compileOperand(b.R)
			idx := c.emit(op, rL, rR, 0)
			if rt {
				c.release(1)
			}
			if lt {
				c.release(1)
			}
			return idx
		}
	}
	condReg := c.reserve()
	c.compileExpr(cond, condReg)
	c.release(1)
	return c.emit(OpJumpIfFalsy, condReg, 0, 0)
}

// branchFalseOp maps a comparison token to the fused jump-if-false opcode.
func branchFalseOp(op token.Kind) (Op, bool) {
	switch op {
	case token.LT:
		return OpJmpFalseLt, true
	case token.LE:
		return OpJmpFalseLe, true
	case token.GT:
		return OpJmpFalseGt, true
	case token.GE:
		return OpJmpFalseGe, true
	case token.EQ:
		return OpJmpFalseEq, true
	case token.NE:
		return OpJmpFalseNe, true
	}
	return 0, false
}

// konst interns a compile-time constant and returns its pool index.
func (c *compiler) konst(v value.Value) int32 {
	key := constKey(v)
	if idx, ok := c.ckeys[key]; ok {
		return idx
	}
	idx := int32(len(c.consts))
	c.consts = append(c.consts, v)
	c.ckeys[key] = idx
	return idx
}

// addTemplate records a nested function/lambda, pre-compiling its body so a
// VM-created closure reuses the bytecode (Proto stays nil if it doesn't compile,
// and that closure tree-walks).
func (c *compiler) addTemplate(name string, params []string, defaults []ast.Expr, body *ast.Block) int32 {
	t := &FuncTemplate{Name: name, Params: params, Defaults: defaults, Body: body}
	if proto, ok := compileFunctionBody(params, body, c.shadowed); ok {
		t.Proto = proto
	}
	idx := int32(len(c.protos))
	c.protos = append(c.protos, t)
	return idx
}

func constKey(v value.Value) string {
	switch v.Tag() {
	case value.Int:
		return "i" + strconv.FormatInt(v.AsInt(), 10)
	case value.Float:
		return "f" + strconv.FormatFloat(v.AsFloat(), 'g', -1, 64)
	case value.Str:
		return "s" + v.AsStr()
	case value.Bool:
		if v.Truthy() {
			return "b1"
		}
		return "b0"
	}
	return "n" // nil
}

// compileStmt emits s. When resultReg >= 0 the statement leaves its value there.
func (c *compiler) compileStmt(s ast.Stmt, resultReg int32) {
	if !c.ok {
		return
	}
	c.setPos(s)
	switch n := s.(type) {
	case *ast.DeclStmt:
		c.compileDecl(n, resultReg)
	case *ast.AssignStmt:
		c.compileAssign(n, resultReg)
	case *ast.ExprStmt:
		r, tmp := c.resultOrTemp(resultReg)
		c.compileExpr(n.X, r)
		c.freeTemp(tmp)
	case *ast.IfStmt:
		c.compileIf(n, resultReg)
	case *ast.WhileStmt:
		c.compileWhile(n, resultReg)
	case *ast.ForStmt:
		c.compileFor(n, resultReg)
	case *ast.ReturnStmt:
		r := c.reserve()
		if n.Value != nil {
			c.compileExpr(n.Value, r)
		} else {
			c.emit(OpLoadNil, r, 0, 0)
		}
		c.emit(OpReturn, r, 0, 0)
		c.release(1)
	case *ast.BreakStmt:
		if len(c.loops) == 0 {
			c.fail() // parser forbids this; fall back if it ever appears
			return
		}
		lp := c.loops[len(c.loops)-1]
		c.emitScopePops(lp.scopeDepth)
		j := c.emit(OpJump, 0, 0, 0) // target patched in popLoop
		lp.breakJumps = append(lp.breakJumps, j)
	case *ast.NextStmt:
		if len(c.loops) == 0 {
			c.fail()
			return
		}
		lp := c.loops[len(c.loops)-1]
		c.emitScopePops(lp.scopeDepth)
		c.emit(OpJump, 0, lp.continueTarget, 0)
	case *ast.FnDecl:
		// Register-eligible functions never contain a nested FnDecl, so this only
		// runs in Env mode (where OpDeclVar binds into the per-call/global env).
		idx := c.addTemplate(n.Name, n.Params, n.Defaults, n.Body)
		r := c.reserve()
		c.emit(OpMakeClosure, r, idx, 0)
		c.emit(OpDeclVar, r, c.konst(value.MakeStr(n.Name)), 0)
		c.release(1)
		if resultReg >= 0 {
			c.emit(OpLoadNil, resultReg, 0, 0) // a declaration yields nil
		}
	case *ast.ExampleStmt:
		// A `drang test` assertion is a no-op in a normal run.
		if resultReg >= 0 {
			c.emit(OpLoadNil, resultReg, 0, 0)
		}
	default:
		c.fail()
	}
}

// compileFor compiles for-in. The iterator (OpIterNew) snapshots arrays/maps/
// strings (so body mutation can't disturb iteration, like the walker) but stays
// lazy over ranges (no materialization). In register mode the loop var(s) are
// register slots scoped to the loop and reused each iteration (safe: a
// register-eligible function is capture-free, so no closure observes the reuse).
// In Env mode each iteration binds the loop var(s) in a fresh child scope, so a
// closure created in the body captures that iteration's binding.
func (c *compiler) compileFor(n *ast.ForStmt, resultReg int32) {
	two := len(n.Vars) == 2
	iterSlot := c.numIters
	c.numIters++
	iterReg := c.reserve()
	c.compileExpr(n.Iter, iterReg)
	c.emit(OpIterNew, iterSlot, iterReg, 0)
	c.release(1) // the iterator captured the iterable; iterReg is free now

	if c.regMode {
		c.enterBlockScope() // loop scope: holds the loop var name(s) and body locals
		var0 := c.declareLocal(n.Vars[0])
		if two {
			c.declareLocal(n.Vars[1]) // == var0+1
		}
		loopStart := int32(len(c.code))
		var jnext int
		if two {
			jnext = c.emit(OpIterNext2, iterSlot, var0, 0)
		} else {
			jnext = c.emit(OpIterNext1, iterSlot, var0, 0)
		}
		c.pushLoop(loopStart) // next -> advance the iterator
		c.compileBlock(n.Body, -1)
		c.emit(OpJump, 0, loopStart, 0)
		exit := int32(len(c.code)) // exit target lives in operand C
		c.code[jnext].C = exit
		c.popLoop(exit) // break -> loop exit
		c.leaveBlockScope()
	} else {
		dstBase := c.reserve() // first/value (persists through the loop)
		if two {
			c.reserve() // second -> dstBase+1
		}
		loopStart := int32(len(c.code))
		var jnext int
		if two {
			jnext = c.emit(OpIterNext2, iterSlot, dstBase, 0)
		} else {
			jnext = c.emit(OpIterNext1, iterSlot, dstBase, 0)
		}
		c.pushLoop(loopStart)        // next -> advance the iterator (snapshot scopeDepth pre-push)
		c.emit(OpPushScope, 0, 0, 0) // a fresh child env per iteration
		c.scopeDepth++
		c.emit(OpDeclVar, dstBase, c.konst(value.MakeStr(n.Vars[0])), 0)
		if two {
			c.emit(OpDeclVar, dstBase+1, c.konst(value.MakeStr(n.Vars[1])), 0)
		}
		c.compileBlock(n.Body, -1)
		c.emit(OpPopScope, 0, 0, 0)
		c.scopeDepth--
		c.emit(OpJump, 0, loopStart, 0)
		exit := int32(len(c.code))
		c.code[jnext].C = exit
		c.popLoop(exit) // break -> loop exit
		if two {
			c.release(2)
		} else {
			c.release(1)
		}
	}
	if resultReg >= 0 {
		c.emit(OpLoadNil, resultReg, 0, 0) // a for loop yields nil
	}
}

func (c *compiler) compileDecl(n *ast.DeclStmt, resultReg int32) {
	if c.regMode {
		// The initializer is evaluated before the name is in scope, so $x := $x + 1
		// reads the outer/free $x. The value lands in the slot that declareLocal
		// then claims (top == localTop here, so tmp == the new slot).
		tmp := c.reserve()
		c.compileExpr(n.Value, tmp)
		c.release(1)
		slot := c.declareLocal(n.Name)
		if slot != tmp {
			c.emit(OpMove, slot, tmp, 0)
		}
		if resultReg >= 0 && resultReg != slot {
			c.emit(OpMove, resultReg, slot, 0)
		}
		return
	}
	r, tmp := c.resultOrTemp(resultReg)
	c.compileExpr(n.Value, r)
	frozen := int32(0)
	if n.Const {
		frozen = 1
	}
	c.emit(OpDeclVar, r, c.konst(value.MakeStr(n.Name)), frozen)
	c.freeTemp(tmp)
}

func (c *compiler) compileAssign(n *ast.AssignStmt, resultReg int32) {
	switch t := n.Target.(type) {
	case *ast.Var:
		c.compileVarAssign(t.Name, n.Op, n.Value, resultReg)
	case *ast.Index:
		c.compileSlotAssign(t.X, t.Idx, "", false, n.Op, n.Value, resultReg)
	case *ast.Field:
		c.compileSlotAssign(t.X, nil, t.Name, true, n.Op, n.Value, resultReg)
	default:
		c.fail()
	}
}

// compileVarAssign compiles $x = rhs and $x op= rhs, for a register-local or an
// env-resident variable.
func (c *compiler) compileVarAssign(name string, op token.Kind, rhs ast.Expr, resultReg int32) {
	if slot, isLocal := c.resolveLocal(name); isLocal {
		if op == token.ILLEGAL {
			c.compileExpr(rhs, slot) // plain: compute straight into the slot
		} else if kc, isLit := literalConst(rhs); isLit {
			c.emit(OpCompoundLocalK, slot, c.konst(kc), int32(op)) // $i += 1
		} else {
			vreg := c.reserve()
			c.compileExpr(rhs, vreg)
			c.release(1)
			c.emit(OpCompoundLocal, slot, vreg, int32(op))
		}
		if resultReg >= 0 && resultReg != slot {
			c.emit(OpMove, resultReg, slot, 0)
		}
		return
	}
	// Free variable: OpSetVar/OpCompoundVar reassign the nearest existing env
	// binding (and error if undefined), exactly like the walker's assignVar.
	if op == token.ILLEGAL {
		r, tmp := c.resultOrTemp(resultReg)
		c.compileExpr(rhs, r)
		c.emit(OpSetVar, r, c.konst(value.MakeStr(name)), 0)
		c.freeTemp(tmp)
		return
	}
	vreg := c.reserve()
	c.compileExpr(rhs, vreg)
	c.emit(OpCompoundVar, c.konst(value.MakeStr(name)), vreg, int32(op)) // new value -> vreg
	if resultReg >= 0 && resultReg != vreg {
		c.emit(OpMove, resultReg, vreg, 0)
	}
	c.release(1)
}

// compileSlotAssign compiles $a[i] = rhs and $m.f = rhs (plus compound forms),
// with autovivification — at any depth ($a[i][j], $cfg.a.b). Evaluation order
// matches evalAssign: rhs, then the final key, then resolve the container chain
// (which evaluates inner keys outside-in), then assign.
func (c *compiler) compileSlotAssign(baseExpr, keyExpr ast.Expr, fieldName string, isField bool, op token.Kind, rhs ast.Expr, resultReg int32) {
	saveTop := c.top
	kreg := c.reserve() // key
	vreg := c.reserve() // value (kreg+1); OpAssignSlot reads the pair, writes its result to vreg
	c.compileExpr(rhs, vreg)
	if isField {
		c.emit(OpLoadConst, kreg, c.konst(value.MakeStr(fieldName)), 0)
	} else {
		c.compileExpr(keyExpr, kreg)
	}
	containerReg, ok := c.compileResolveContainer(baseExpr, kreg)
	if !ok {
		c.top = saveTop
		c.fail() // unsupported base (e.g. a call) -> whole unit falls back
		return
	}
	c.emit(OpAssignSlot, containerReg, kreg, int32(op))
	if resultReg >= 0 {
		c.emit(OpMove, resultReg, vreg, 0)
	}
	c.top = saveTop // release the key/value pair and all container-resolution temps
}

// compileResolveContainer emits code that resolves e to the container to write
// through, autovivifying nil links along the way, and returns the register holding
// it. kindKeyReg holds the key that indexes into e, so that if e is nil it
// autovivifies as kindFor(that key) — an int key makes an array, anything else a
// map. It mirrors the walker's recursive resolveContainer; OpResolveSlot reuses its
// key register as the output to stay within three operands.
func (c *compiler) compileResolveContainer(e ast.Expr, kindKeyReg int32) (int32, bool) {
	switch t := e.(type) {
	case *ast.Var:
		if slot, isLocal := c.resolveLocal(t.Name); isLocal {
			c.emit(OpResolveLocalContainer, slot, kindKeyReg, c.konst(value.MakeStr(t.Name)))
			return slot, true
		}
		creg := c.reserve()
		c.emit(OpResolveVarContainer, creg, c.konst(value.MakeStr(t.Name)), kindKeyReg)
		return creg, true
	case *ast.Index:
		keyReg := c.reserve()
		c.compileExpr(t.Idx, keyReg)
		parentReg, ok := c.compileResolveContainer(t.X, keyReg) // parent autoviv kind = kindFor(this key)
		if !ok {
			return 0, false
		}
		c.emit(OpResolveSlot, keyReg, kindKeyReg, parentReg) // result -> keyReg
		return keyReg, true
	case *ast.Field:
		nameReg := c.reserve()
		c.emit(OpLoadConst, nameReg, c.konst(value.MakeStr(t.Name)), 0)
		parentReg, ok := c.compileResolveContainer(t.X, nameReg) // a string key -> parent kind = map
		if !ok {
			return 0, false
		}
		c.emit(OpResolveSlot, nameReg, kindKeyReg, parentReg)
		return nameReg, true
	}
	return 0, false // unsupported base
}

// compileBlock compiles a block, leaving its value (its last statement's value)
// in resultReg when resultReg >= 0.
func (c *compiler) compileBlock(b *ast.Block, resultReg int32) {
	if len(b.Stmts) == 0 {
		if resultReg >= 0 {
			c.emit(OpLoadNil, resultReg, 0, 0)
		}
		return
	}
	for i, s := range b.Stmts {
		rr := int32(-1)
		if i == len(b.Stmts)-1 {
			rr = resultReg
		}
		c.compileStmt(s, rr)
		if !c.ok {
			return
		}
	}
}

func (c *compiler) compileIf(n *ast.IfStmt, resultReg int32) {
	jfalse := c.compileBranchFalse(n.Cond)
	c.enterBlockScope()
	c.compileBlock(n.Then, resultReg)
	c.leaveBlockScope()
	jend := c.emit(OpJump, 0, 0, 0)
	c.patchBranch(jfalse) // false branch starts here
	switch e := n.Else.(type) {
	case nil:
		if resultReg >= 0 {
			c.emit(OpLoadNil, resultReg, 0, 0) // no branch taken -> nil value
		}
	case *ast.Block:
		c.enterBlockScope()
		c.compileBlock(e, resultReg)
		c.leaveBlockScope()
	case *ast.IfStmt:
		c.compileIf(e, resultReg) // else-if: no extra scope, matching evalIf
	default:
		c.fail()
	}
	c.patchJump(jend)
}

func (c *compiler) compileWhile(n *ast.WhileStmt, resultReg int32) {
	if resultReg >= 0 {
		c.emit(OpLoadNil, resultReg, 0, 0) // a while loop yields nil
	}
	loopStart := int32(len(c.code))
	jexit := c.compileBranchFalse(n.Cond) // cond runs in the outer scope, like the walker
	c.pushLoop(loopStart)                 // next -> re-check the condition
	c.enterBlockScope()                   // a fresh scope per iteration
	c.compileBlock(n.Body, -1)
	c.leaveBlockScope()
	c.emit(OpJump, 0, loopStart, 0)
	c.patchBranch(jexit)
	c.popLoop(int32(len(c.code))) // break -> loop exit
}

// compileExpr emits code that leaves e's value in register dst. Temporaries are
// reserved above the current top and released before the producing op, so the op
// reads them while they still hold their values.
func (c *compiler) compileExpr(e ast.Expr, dst int32) {
	if !c.ok {
		return
	}
	c.setPos(e)
	switch n := e.(type) {
	case *ast.IntLit:
		c.emit(OpLoadConst, dst, c.konst(value.MakeInt(n.Value)), 0)
	case *ast.FloatLit:
		c.emit(OpLoadConst, dst, c.konst(value.MakeFloat(n.Value)), 0)
	case *ast.StringLit:
		c.emit(OpLoadConst, dst, c.konst(value.MakeStr(n.Value)), 0)
	case *ast.Interp:
		// Compile as the equivalent "" ~ p0 ~ p1 ~ ... fold (the pre-Interp desugaring),
		// so interpolated strings stay on the VM with byte-identical bytecode.
		var expr ast.Expr = n.Parts[0]
		if _, isStr := expr.(*ast.StringLit); !isStr {
			expr = &ast.Binary{Pos: n.Pos, Op: token.TILDE, L: &ast.StringLit{Pos: n.Pos}, R: expr}
		}
		for _, op := range n.Parts[1:] {
			expr = &ast.Binary{Pos: n.Pos, Op: token.TILDE, L: expr, R: op}
		}
		c.compileExpr(expr, dst)
	case *ast.BoolLit:
		c.emit(OpLoadConst, dst, c.konst(value.MakeBool(n.Value)), 0)
	case *ast.RegexLit:
		// The pattern is a string constant; OpMakeRegex compiles it at runtime via
		// the shared cache (compile-once across executions), so the constant pool
		// stays scalar and the regex value is never deduped by pattern.
		c.emit(OpMakeRegex, dst, c.konst(value.MakeStr(n.Pattern)), 0)
	case *ast.Var:
		if slot, ok := c.resolveLocal(n.Name); ok {
			if dst != slot {
				c.emit(OpMove, dst, slot, 0)
			}
		} else {
			c.emit(OpGetVar, dst, c.konst(value.MakeStr(n.Name)), 0)
		}
	case *ast.Ident:
		// A bare identifier shares the namespace with $-vars (the walker's
		// env.get is sigil-blind), so a register-local of the same name wins.
		if slot, ok := c.resolveLocal(n.Name); ok {
			if dst != slot {
				c.emit(OpMove, dst, slot, 0)
			}
		} else {
			c.emit(OpGetIdent, dst, c.konst(value.MakeStr(n.Name)), 0)
		}
	case *ast.Unary:
		switch n.Op {
		case token.MINUS, token.BANG:
			r, t := c.compileOperand(n.X)
			if n.Op == token.MINUS {
				c.emit(OpNeg, dst, r, 0)
			} else {
				c.emit(OpNot, dst, r, 0)
			}
			if t {
				c.release(1)
			}
		default:
			c.fail()
		}
	case *ast.Binary:
		op, ok := binOp(n.Op)
		if !ok {
			c.fail()
			return
		}
		if kop, hasK := binOpK(n.Op); hasK {
			// const on the right: $n - 1, $i % 7, $s ~ "\n"
			if kc, isLit := literalConst(n.R); isLit {
				rL, lt := c.compileOperand(n.L)
				c.emit(kop, dst, rL, c.konst(kc))
				if lt {
					c.release(1)
				}
				return
			}
			// const on the left of a commutative op: 2 * $x  ==  $x * 2
			if commutativeOp(n.Op) {
				if kc, isLit := literalConst(n.L); isLit {
					rR, rt := c.compileOperand(n.R)
					c.emit(kop, dst, rR, c.konst(kc))
					if rt {
						c.release(1)
					}
					return
				}
			}
		}
		rL, lt := c.compileOperand(n.L)
		rR, rt := c.compileOperand(n.R)
		c.emit(op, dst, rL, rR)
		if rt {
			c.release(1)
		}
		if lt {
			c.release(1)
		}
	case *ast.Logical:
		c.compileExpr(n.L, dst)
		var jmp int
		if n.Op == token.OR {
			jmp = c.emit(OpJumpIfTruthy, dst, 0, 0) // keep first truthy operand
		} else {
			jmp = c.emit(OpJumpIfFalsy, dst, 0, 0) // keep first falsy operand
		}
		c.compileExpr(n.R, dst)
		c.patchJump(jmp)
	case *ast.Call:
		nargs := int32(len(n.Args))
		base := c.top
		for _, a := range n.Args { // args are evaluated first, in both callee forms
			r := c.reserve()
			c.compileExpr(a, r)
		}
		c.setPos(n) // arg compilation moved curPos; the call itself is at n
		if id, ok := n.Callee.(*ast.Ident); ok {
			c.release(nargs)
			c.ensureReg(base) // result lands in the arg base
			if c.shadowed != nil && isNonUserName(id.Name) && !c.shadowed[id.Name] {
				// Provably never a user binding -> skip the env lookup.
				c.emit(OpCallBuiltin, base, nargs, c.konst(value.MakeStr(id.Name)))
			} else {
				c.emit(OpCall, base, nargs, c.konst(value.MakeStr(id.Name)))
			}
		} else {
			// Non-identifier callee: evaluate it to a function value, then call it.
			calleeReg := c.reserve() // base+nargs
			c.compileExpr(n.Callee, calleeReg)
			c.release(nargs + 1)
			c.ensureReg(base)
			c.emit(OpCallValue, base, nargs, calleeReg)
		}
		if dst != base {
			c.emit(OpMove, dst, base, 0)
		}
	case *ast.Pipe:
		// Compile as the equivalent call f(lhs, args...) — identical bytecode to the
		// old parse-time desugaring, so |> stays on the VM (no tree-walker fallback).
		c.compileExpr(&ast.Call{Pos: n.Call.Pos, Callee: n.Call.Callee, Args: append([]ast.Expr{n.Lhs}, n.Call.Args...)}, dst)
	case *ast.Lambda:
		idx := c.addTemplate("", n.Params, n.Defaults, n.Body)
		c.emit(OpMakeClosure, dst, idx, 0)
	case *ast.ArrayLit:
		count := int32(len(n.Elems))
		base := c.top
		for _, el := range n.Elems {
			r := c.reserve()
			c.compileExpr(el, r)
		}
		c.release(count)
		c.emit(OpMakeArray, dst, base, count)
	case *ast.MapLit:
		count := int32(len(n.Keys))
		base := c.top
		for i := range n.Keys {
			kr := c.reserve()
			if id, ok := n.Keys[i].(*ast.Ident); ok {
				// A bare identifier key is its name as a string ({cwd: x} == {"cwd": x}).
				c.emit(OpLoadConst, kr, c.konst(value.MakeStr(id.Name)), 0)
			} else {
				c.compileExpr(n.Keys[i], kr)
			}
			vr := c.reserve()
			c.compileExpr(n.Vals[i], vr)
		}
		c.release(2 * count)
		c.emit(OpMakeMap, dst, base, count)
	case *ast.RangeLit:
		t1 := c.reserve()
		c.compileExpr(n.Lo, t1)
		t2 := c.reserve()
		c.compileExpr(n.Hi, t2)
		c.release(2)
		c.emit(OpMakeRange, dst, t1, t2)
	case *ast.Index:
		t1 := c.reserve()
		c.compileExpr(n.X, t1)
		t2 := c.reserve()
		c.compileExpr(n.Idx, t2)
		c.release(2)
		c.emit(OpIndex, dst, t1, t2)
	case *ast.Field:
		t := c.reserve()
		c.compileExpr(n.X, t)
		c.release(1)
		c.emit(OpField, dst, t, c.konst(value.MakeStr(n.Name)))
	case *ast.Propagate:
		c.compileExpr(n.X, dst)
		c.emit(OpPropagate, dst, 0, 0) // if dst is an error, unwind; else dst passes through
	case *ast.DefOr:
		c.compileExpr(n.X, dst)
		jskip := c.emit(OpJumpIfDefined, dst, 0, 0) // dst is a real value -> keep it
		c.compileExpr(n.Fallback, dst)              // dst was nil/error -> use the fallback
		c.patchJump(jskip)
	default:
		// Ident-as-value: not yet compiled -> the enclosing unit falls back.
		c.fail()
	}
}

// compileOperand returns a register holding e's value, avoiding a copy when e is
// already a register-resident local (move elision). The bool reports whether a
// temporary was allocated and must be released by the caller. Reading a local at
// the operator (rather than copying it first) is safe: a register-local can't be
// mutated by an intervening evaluation — a callee gets its own frame, and
// register-eligible functions are capture-free.
func (c *compiler) compileOperand(e ast.Expr) (int32, bool) {
	if v, ok := e.(*ast.Var); ok {
		if slot, isLocal := c.resolveLocal(v.Name); isLocal {
			return slot, false
		}
	}
	t := c.reserve()
	c.compileExpr(e, t)
	return t, true
}

// literalConst returns a literal expression's compile-time constant value.
func literalConst(e ast.Expr) (value.Value, bool) {
	switch n := e.(type) {
	case *ast.IntLit:
		return value.MakeInt(n.Value), true
	case *ast.FloatLit:
		return value.MakeFloat(n.Value), true
	case *ast.StringLit:
		return value.MakeStr(n.Value), true
	case *ast.BoolLit:
		return value.MakeBool(n.Value), true
	}
	return value.Value{}, false
}

// binOpK maps an arithmetic/concat token to its constant-immediate opcode.
// Comparisons are omitted: condition comparisons already fuse via compileBranchFalse.
func binOpK(op token.Kind) (Op, bool) {
	switch op {
	case token.PLUS:
		return OpAddK, true
	case token.MINUS:
		return OpSubK, true
	case token.STAR:
		return OpMulK, true
	case token.SLASH:
		return OpDivK, true
	case token.PERCENT:
		return OpModK, true
	case token.TILDE:
		return OpConcatK, true
	}
	return 0, false
}

// commutativeOp reports ops for which a left-hand constant can be swapped to the
// right (so the K-form fires) without changing the result.
func commutativeOp(op token.Kind) bool {
	return op == token.PLUS || op == token.STAR
}

// binOp maps an infix token to its arithmetic/comparison/concat opcode.
func binOp(op token.Kind) (Op, bool) {
	switch op {
	case token.PLUS:
		return OpAdd, true
	case token.MINUS:
		return OpSub, true
	case token.STAR:
		return OpMul, true
	case token.SLASH:
		return OpDiv, true
	case token.PERCENT:
		return OpMod, true
	case token.TILDE:
		return OpConcat, true
	case token.EQ:
		return OpEq, true
	case token.NE:
		return OpNe, true
	case token.LT:
		return OpLt, true
	case token.LE:
		return OpLe, true
	case token.GT:
		return OpGt, true
	case token.GE:
		return OpGe, true
	case token.SPACESHIP:
		return OpSpaceship, true
	}
	return 0, false
}
