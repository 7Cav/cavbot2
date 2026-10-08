// Command errorreply reports a member error reply that carries an error's
// text. The gate runs it over the module (.github/scripts/gate.sh).
package main

import (
	"go/token"
	"go/types"
	"slices"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/analysis/singlechecker"
	"golang.org/x/tools/go/ssa"
)

var analyzer = &analysis.Analyzer{
	Name:     "errorreply",
	Doc:      "reports a utils.HandleError call whose message carries an error's text",
	Requires: []*analysis.Analyzer{buildssa.Analyzer},
	Run:      run,
}

func main() { singlechecker.Main(analyzer) }

// The function that sends a member its error reply, and the position of the
// parameter that carries the reply's text.
const (
	sinkPackage = "github.com/7cav/cavbot2/utils"
	sinkName    = "HandleError"
	sinkParam   = 2
)

const diagnostic = "this member reply carries an error's text; reply with a fixed message and log the error or capture it (ADR 0001)"

var errorType = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

func run(pass *analysis.Pass) (any, error) {
	funcs := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA).SrcFuncs
	// A function of this package that passes one of its parameters on to a
	// sink is a sink for that parameter too. Each round over the package
	// finds the wrappers of the sinks known so far, until a round finds none.
	wrappers := map[*ssa.Function]map[int]bool{}
	reported := map[token.Pos]bool{}
	for found := true; found; {
		found = false
		for _, fn := range funcs {
			for _, block := range fn.Blocks {
				for _, instr := range block.Instrs {
					call, ok := instr.(ssa.CallInstruction)
					if !ok {
						continue
					}
					for _, param := range sinkParams(call.Common(), wrappers) {
						t := &tracer{seen: map[visit]bool{}}
						if t.trace(call.Common().Args[param], nil) && !reported[call.Pos()] {
							reported[call.Pos()] = true
							pass.Reportf(call.Pos(), diagnostic)
						}
						for _, forwarded := range t.forwarded {
							if wrappers[fn] == nil {
								wrappers[fn] = map[int]bool{}
							}
							if !wrappers[fn][forwarded] {
								wrappers[fn][forwarded] = true
								found = true
							}
						}
					}
				}
			}
		}
	}
	return nil, nil
}

// sinkParams returns the positions of call's arguments that reach a member
// as their error reply.
func sinkParams(call *ssa.CallCommon, wrappers map[*ssa.Function]map[int]bool) []int {
	callee := call.StaticCallee()
	if callee == nil {
		return nil
	}
	if callee.Name() == sinkName && callee.Pkg != nil && callee.Pkg.Pkg.Path() == sinkPackage && len(call.Args) > sinkParam {
		return []int{sinkParam}
	}
	var params []int
	for param := range wrappers[callee] {
		params = append(params, param)
	}
	return params
}

// tracer reports whether any value a value is built from is an error. It
// follows a call into a function of the package being checked, through its
// return values and back out through its parameters to the call's
// arguments. A call into any other function counts as built from all its
// arguments.
type tracer struct {
	seen map[visit]bool
	// forwarded holds the positions of the traced function's own parameters
	// the value was built from.
	forwarded []int
}

// visit is a value traced inside the chain of calls f entered to reach it.
type visit struct {
	v ssa.Value
	f *frame
}

// frame is a call the tracer followed into its callee's body.
type frame struct {
	call   *ssa.Call
	parent *frame
}

func (f *frame) entered(fn *ssa.Function) bool {
	for ; f != nil; f = f.parent {
		if f.call.Call.StaticCallee() == fn {
			return true
		}
	}
	return false
}

func (t *tracer) trace(v ssa.Value, f *frame) bool {
	if v == nil || t.seen[visit{v, f}] {
		return false
	}
	t.seen[visit{v, f}] = true
	if types.Implements(v.Type(), errorType) {
		return true
	}
	switch v := v.(type) {
	case *ssa.Call:
		if callee := v.Call.StaticCallee(); callee != nil && callee.Blocks != nil && !f.entered(callee) {
			return t.traceReturns(callee, &frame{call: v, parent: f})
		}
		if v.Call.IsInvoke() && t.trace(v.Call.Value, f) {
			return true
		}
		for _, arg := range v.Call.Args {
			if t.trace(arg, f) {
				return true
			}
		}
	case *ssa.Parameter:
		index := slices.Index(v.Parent().Params, v)
		if f == nil {
			t.forwarded = append(t.forwarded, index)
			return false
		}
		if f.call.Call.StaticCallee() == v.Parent() {
			return t.trace(f.call.Call.Args[index], f.parent)
		}
	case *ssa.BinOp:
		return t.trace(v.X, f) || t.trace(v.Y, f)
	case *ssa.Phi:
		for _, edge := range v.Edges {
			if t.trace(edge, f) {
				return true
			}
		}
	case *ssa.Slice:
		return t.trace(v.X, f)
	case *ssa.MakeInterface:
		return t.trace(v.X, f)
	case *ssa.ChangeInterface:
		return t.trace(v.X, f)
	case *ssa.Alloc:
		return t.traceStores(v, f)
	case *ssa.FreeVar:
		return t.traceBindings(v, f)
	case *ssa.UnOp:
		return t.trace(v.X, f)
	case *ssa.FieldAddr:
		return t.trace(v.X, f)
	}
	return false
}

// traceReturns traces every value fn returns, inside the call f entered.
func (t *tracer) traceReturns(fn *ssa.Function, f *frame) bool {
	for _, block := range fn.Blocks {
		ret, ok := block.Instrs[len(block.Instrs)-1].(*ssa.Return)
		if !ok {
			continue
		}
		for _, result := range ret.Results {
			if t.trace(result, f) {
				return true
			}
		}
	}
	return false
}

// traceBindings traces what each closure the enclosing function makes binds
// to the free variable v.
func (t *tracer) traceBindings(v *ssa.FreeVar, f *frame) bool {
	closure := v.Parent()
	index := slices.Index(closure.FreeVars, v)
	if closure.Parent() == nil {
		return false
	}
	for _, block := range closure.Parent().Blocks {
		for _, instr := range block.Instrs {
			if mk, ok := instr.(*ssa.MakeClosure); ok && mk.Fn == closure && t.trace(mk.Bindings[index], f) {
				return true
			}
		}
	}
	return false
}

// traceStores traces every value stored at addr or at an address within it,
// and every argument of a call that addr is passed to, since the call may
// write them there, as a strings.Builder's WriteString does.
func (t *tracer) traceStores(addr ssa.Value, f *frame) bool {
	refs := addr.Referrers()
	if refs == nil {
		return false
	}
	for _, ref := range *refs {
		switch ref := ref.(type) {
		case *ssa.Store:
			if ref.Addr == addr && t.trace(ref.Val, f) {
				return true
			}
		case ssa.CallInstruction:
			for _, arg := range ref.Common().Args {
				if t.trace(arg, f) {
					return true
				}
			}
		case *ssa.IndexAddr, *ssa.FieldAddr:
			if t.traceStores(ref.(ssa.Value), f) {
				return true
			}
		}
	}
	return false
}
