package eval

import (
	"github.com/anafalanx/lang3/internal/ast"
	"github.com/anafalanx/lang3/internal/value"
)

// Op is a VM opcode. The register machine is 3-address: most ops read operand
// registers B and C and write register A. The set is deliberately small and the
// constants are dense so Go's compiler emits a jump table for the dispatch switch
// (Go has no computed-goto, so a dense switch is the fastest portable dispatch).
type Op uint8

const (
	OpLoadConst   Op = iota // R[A] = Consts[B]
	OpLoadNil               // R[A] = nil
	OpMove                  // R[A] = R[B]
	OpAdd                   // R[A] = R[B] + R[C]
	OpSub                   // R[A] = R[B] - R[C]
	OpMul                   // R[A] = R[B] * R[C]
	OpDiv                   // R[A] = R[B] / R[C]
	OpMod                   // R[A] = R[B] % R[C]
	OpConcat                // R[A] = R[B] ~ R[C]
	// Constant-immediate fast paths: right operand is a pooled constant, folding
	// away a LoadConst + a temporary register. (Commutative + and * also fire when
	// the constant is on the left.)
	OpAddK    // R[A] = R[B] + Consts[C]
	OpSubK    // R[A] = R[B] - Consts[C]
	OpMulK    // R[A] = R[B] * Consts[C]
	OpDivK    // R[A] = R[B] / Consts[C]
	OpModK    // R[A] = R[B] % Consts[C]
	OpConcatK // R[A] = R[B] ~ Consts[C]
	OpEq                    // R[A] = R[B] == R[C]
	OpNe                    // R[A] = R[B] != R[C]
	OpLt                    // R[A] = R[B] < R[C]
	OpLe                    // R[A] = R[B] <= R[C]
	OpGt                    // R[A] = R[B] > R[C]
	OpGe                    // R[A] = R[B] >= R[C]
	OpSpaceship             // R[A] = R[B] <=> R[C]   (-1/0/1)
	OpNeg                   // R[A] = -R[B]
	OpNot                   // R[A] = !R[B]
	OpMakeArray             // R[A] = [R[B] .. R[B+C])            (C = element count)
	OpMakeMap               // R[A] = {pairs in R[B .. B+2C)}     (C = pair count; an unhashable key -> Err)
	OpMakeRange             // R[A] = R[B]..R[C]                  (non-int bounds -> Err)
	OpIndex                 // R[A] = R[B][R[C]]
	OpField                 // R[A] = R[B].(Consts[C])
	OpResolveLocalContainer // autoviv local slot A as a container of kindFor(R[B]); name=Consts[C] (errors)
	OpResolveVarContainer   // R[A] = env var Consts[B] as a container of kindFor(R[C]) (autoviv in env)
	OpAssignSlot            // R[A][R[B]] op= R[B+1]; op=token.Kind(C); new value -> R[B+1]
	OpResolveSlot           // R[A] = R[C][R[A]] as a container, autoviv kindFor(R[B]) (nested write path)
	OpCompoundLocal         // R[A] = compound(token.Kind(C), R[A], R[B])           (local $x op= rhs)
	OpCompoundLocalK        // R[A] = compound(token.Kind(C), R[A], Consts[B])      (local $x op= const)
	OpCompoundVar           // env var Consts[A] op= R[B]; op=token.Kind(C); new value -> R[B]
	OpIterNew               // iters[A] = iterator over R[B]                       (for-in; errors if not iterable)
	OpIterNext1             // advance iters[A]: R[B] = value, else ip = C          (one-var for-in)
	OpIterNext2             // advance iters[A]: R[B] = key/idx, R[B+1] = value, else ip = C (two-var)
	OpGetVar                // R[A] = env.get(Consts[B])           (Env-backed; v1)
	OpDeclVar               // env.define(Consts[B], R[A], C != 0)  (C = frozen flag)
	OpSetVar                // env.set(Consts[B], R[A])
	OpJump                  // ip = B
	OpJumpIfFalsy           // if !truthy(R[A]) { ip = B }
	OpJumpIfTruthy          // if truthy(R[A])  { ip = B }
	OpJumpIfDefined         // if R[A] is neither nil nor error { ip = B }   (for //)
	// Fused compare-and-branch (if/while conditions): compute R[A] <cmp> R[B] and
	// jump to C when the comparison is FALSE. Computes the exact op (not its
	// inverse), so NaN behaves identically to the walker.
	OpJmpFalseLt
	OpJmpFalseLe
	OpJmpFalseGt
	OpJmpFalseGe
	OpJmpFalseEq
	OpJmpFalseNe
	OpPushScope             // env = env.child()
	OpPopScope              // env = env.parent
	OpCall                  // R[A] = call(Consts[C], R[A : A+B])   (B = argc; result over the arg base)
	OpCallBuiltin           // like OpCall but the name is a never-shadowed builtin/HOF — skips env.get
	OpCallValue             // R[A] = R[C](R[A : A+B])              (B = argc; callee value in R[C])
	OpGetIdent              // R[A] = env.get(Consts[B])            (bare identifier as a value)
	OpMakeClosure           // R[A] = a *Function from Protos[B], capturing the current env
	OpPropagate             // if R[A] is an error, unwind it as errSignal; else pass through (for ?)
	OpReturn                // return R[A]
)

// Instr is a fixed-width instruction. Fixed width keeps decode branch-free; the
// int32 operands give ample register and jump-target range without bit-packing.
type Instr struct {
	Op      Op
	A, B, C int32
}

// Proto is a compiled program or function body: bytecode plus its constant pool
// and the number of registers a frame needs. value.Value is the register cell, so
// scalars stay unboxed — the property that lets the VM avoid CPython-style boxing.
type Proto struct {
	Code    []Instr
	Consts  []value.Value
	NumRegs   int
	NumIters  int             // for-in iterator slots this proto uses
	Protos    []*FuncTemplate // nested function/lambda templates, by index (OpMakeClosure)
	Positions []ast.Pos       // source position per instruction (parallel to Code), for runtime errors
	// RegLocals marks a function compiled in register mode: params and locals live
	// in registers, so the caller runs it directly in the captured env (no per-call
	// child env) with the arguments preloaded into the low registers.
	RegLocals bool
}

// FuncTemplate is a nested function or lambda the compiler found inside a Proto.
// OpMakeClosure turns it into a *Function at runtime by pairing it with the
// capturing env. Proto is the pre-compiled body, or nil when the body did not
// compile (that closure then tree-walks via Body).
type FuncTemplate struct {
	Name   string
	Params []string
	Body   *ast.Block
	Proto  *Proto
}
