// Command errorreply reports a message the bot sends to Discord that carries
// data from an error: an interaction reply, edit or followup, a channel post,
// or a utils.HandleError reply, checked whole, embeds included. ADR 0016 says
// which data from an error a message may carry. The gate runs the check over
// the module's production code (.github/scripts/gate.sh).
//
// The check reads one package at a time, so it judges a value by what was
// stored in it only for a struct error type the package being checked
// defines. An error type from another package, or one that isn't a struct,
// counts as built from an error on sight. That is stricter than ADR 0016
// until the check follows calls into the module's other packages (#534).
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
	Doc:      "reports a message to Discord that carries data from an error",
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

// The methods that send a message to Discord, on any interface that declares
// them and on *discordgo.Session, and the position of the argument that
// carries the message, not counting the receiver.
var sinkMethods = map[string]int{
	"InteractionRespond":        1,
	"InteractionResponseEdit":   1,
	"FollowupMessageCreate":     2,
	"ChannelMessageSend":        1,
	"ChannelMessageSendComplex": 1,
	"ChannelMessageEditComplex": 0,
}

const discordgoPackage = "github.com/bwmarrin/discordgo"

const diagnostic = "this message to Discord carries data from an error; send fixed text and log the error or capture it (ADR 0016)"

var errorType = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

func run(pass *analysis.Pass) (any, error) {
	funcs := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA).SrcFuncs
	// A function of this package that passes one of its parameters on to a
	// sink is a sink for that parameter too. A value of an error type this
	// package defines carries what is stored in it, so once one reaches a
	// sink, each store into that type is checked. Each round over the package
	// finds the wrappers and the types the sinks known so far reach, until a
	// round finds none.
	wrappers := map[*ssa.Function]map[int]bool{}
	reached := map[*types.TypeName]bool{}
	reported := map[token.Pos]bool{}
	for found := true; found; {
		found = false
		for _, fn := range funcs {
			for _, block := range fn.Blocks {
				for _, instr := range block.Instrs {
					for _, v := range outgoing(instr, wrappers, reached) {
						t := &tracer{pkg: pass.Pkg, seen: map[visit]bool{}}
						if t.trace(v, nil) && !reported[instr.Pos()] {
							reported[instr.Pos()] = true
							pass.Reportf(instr.Pos(), diagnostic)
						}
						for _, forwarded := range t.forwarded {
							// A closure can pass on a parameter of the
							// function that encloses it, which makes that
							// function the wrapper.
							owner := forwarded.Parent()
							index := slices.Index(owner.Params, forwarded)
							if wrappers[owner] == nil {
								wrappers[owner] = map[int]bool{}
							}
							if !wrappers[owner][index] {
								wrappers[owner][index] = true
								found = true
							}
						}
						for _, typ := range t.reached {
							if !reached[typ] {
								reached[typ] = true
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

// outgoing returns the values instr hands on toward Discord: the arguments of
// a sink call that carry the message, or the value stored into a field of an
// error type that reaches a sink.
func outgoing(instr ssa.Instruction, wrappers map[*ssa.Function]map[int]bool, reached map[*types.TypeName]bool) []ssa.Value {
	switch instr := instr.(type) {
	case ssa.CallInstruction:
		var values []ssa.Value
		for _, param := range sinkParams(instr.Common(), wrappers) {
			values = append(values, instr.Common().Args[param])
		}
		return values
	case *ssa.Store:
		for addr := instr.Addr; ; {
			field, ok := addr.(*ssa.FieldAddr)
			if !ok {
				return nil
			}
			if reached[typeName(field.X.Type())] {
				return []ssa.Value{instr.Val}
			}
			addr = field.X
		}
	}
	return nil
}

// sinkParams returns the positions of call's arguments that reach Discord as
// a message.
func sinkParams(call *ssa.CallCommon, wrappers map[*ssa.Function]map[int]bool) []int {
	if call.IsInvoke() {
		if param, ok := sinkMethods[call.Method.Name()]; ok && len(call.Args) > param {
			return []int{param}
		}
		return nil
	}
	callee := call.StaticCallee()
	if callee == nil {
		return nil
	}
	if param, ok := sinkMethods[callee.Name()]; ok && isSessionMethod(callee) && len(call.Args) > param+1 {
		// The receiver is the call's first argument.
		return []int{param + 1}
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

// isSessionMethod reports whether fn is a method of *discordgo.Session.
func isSessionMethod(fn *ssa.Function) bool {
	recv := fn.Signature.Recv()
	return recv != nil && isNamed(recv.Type(), discordgoPackage, "Session")
}

// isNamed reports whether typ, or the type it points to, is one of the types
// names in the package at path.
func isNamed(typ types.Type, path string, names ...string) bool {
	obj := typeName(typ)
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == path && slices.Contains(names, obj.Name())
}

// tracer reports whether any value a value is built from is an error, other
// than a time and a value of an error type the package defines (ADR 0016).
// It follows a call into a function of the package being checked, through
// its return values and back out through its parameters to the call's
// arguments. A call into any other function counts as built from all its
// arguments.
type tracer struct {
	pkg  *types.Package
	seen map[visit]bool
	// forwarded holds the parameters of the traced function, or of a
	// function enclosing it, that the value was built from.
	forwarded []*ssa.Parameter
	// reached holds the error types the package defines that the value was
	// built from. Their stores carry the value's data.
	reached []*types.TypeName
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
	if isTime(v.Type()) {
		return false
	}
	if types.Implements(v.Type(), errorType) {
		if obj := typeName(v.Type()); obj != nil && obj.Pkg() == t.pkg && isStruct(obj) {
			t.reached = append(t.reached, obj)
			return false
		}
		if !t.followable(v, f) {
			return true
		}
	}
	switch v := v.(type) {
	case *ssa.Call:
		if callee := t.callee(v, f); callee != nil {
			return t.traceReturns(callee, &frame{call: v, parent: f}, -1)
		}
		if v.Call.IsInvoke() && t.trace(v.Call.Value, f) {
			return true
		}
		for _, arg := range v.Call.Args {
			if t.trace(arg, f) {
				return true
			}
		}
	case *ssa.Extract:
		if call, ok := v.Tuple.(*ssa.Call); ok {
			if callee := t.callee(call, f); callee != nil {
				return t.traceReturns(callee, &frame{call: call, parent: f}, v.Index)
			}
		}
	case *ssa.Parameter:
		if f == nil {
			t.forwarded = append(t.forwarded, v)
			return false
		}
		if f.call.Call.StaticCallee() == v.Parent() {
			return t.trace(f.call.Call.Args[slices.Index(v.Parent().Params, v)], f.parent)
		}
	case *ssa.BinOp:
		switch v.Op {
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			// A comparison inspects its operands and carries none of them.
			return false
		}
		return t.trace(v.X, f) || t.trace(v.Y, f)
	case *ssa.Phi:
		for _, edge := range v.Edges {
			if t.trace(edge, f) {
				return true
			}
		}
	case *ssa.Slice:
		return t.trace(v.X, f)
	case *ssa.Convert:
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
	case *ssa.Field:
		return t.trace(v.X, f)
	}
	return false
}

// isTime reports whether typ is a time or a duration, which can't carry an
// error's words even when read off an error (ADR 0016).
func isTime(typ types.Type) bool {
	return isNamed(typ, "time", "Time", "Duration")
}

// followable reports whether the tracer can follow the error value v to
// where it was made: v is nil, converted to an interface, or returned by a
// call into the package. Any other error value carries its error's data.
func (t *tracer) followable(v ssa.Value, f *frame) bool {
	switch v := v.(type) {
	case *ssa.Const, *ssa.MakeInterface, *ssa.ChangeInterface, *ssa.Phi:
		return true
	case *ssa.Call:
		return t.callee(v, f) != nil
	case *ssa.Extract:
		call, ok := v.Tuple.(*ssa.Call)
		return ok && t.callee(call, f) != nil
	case *ssa.Parameter:
		return f != nil && f.call.Call.StaticCallee() == v.Parent()
	}
	return false
}

// callee returns the function of the package being checked that call
// enters, or nil when the tracer can't follow call into its body.
func (t *tracer) callee(call *ssa.Call, f *frame) *ssa.Function {
	callee := call.Call.StaticCallee()
	if callee == nil || callee.Blocks == nil || f.entered(callee) {
		return nil
	}
	return callee
}

// typeName returns the name of the named type typ is, or points to.
func typeName(typ types.Type) *types.TypeName {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	if named, ok := typ.(*types.Named); ok {
		return named.Obj()
	}
	return nil
}

func isStruct(obj *types.TypeName) bool {
	_, ok := obj.Type().Underlying().(*types.Struct)
	return ok
}

// traceReturns traces the value fn returns at index, or every value it
// returns when index is negative, inside the call f entered.
func (t *tracer) traceReturns(fn *ssa.Function, f *frame, index int) bool {
	for _, block := range fn.Blocks {
		ret, ok := block.Instrs[len(block.Instrs)-1].(*ssa.Return)
		if !ok {
			continue
		}
		for i, result := range ret.Results {
			if (index < 0 || i == index) && t.trace(result, f) {
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
	for _, ref := range *addr.Referrers() {
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
