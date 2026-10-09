// Command errorreply reports what the bot shows a person that carries data
// from an error. That is a message the bot sends to Discord: an interaction
// reply, edit or followup, a channel post, or a utils.HandleError reply. It is
// also a panel answer: the text of the panel's http.Error, the URL of its
// redirect, or the data its page renders. Each is checked whole, embeds and
// page data included. A Foxhole report member's failure reason and a spawn
// failure's cause reach a panel page through the store or the temp VC
// runtime, where the check can't follow them, so a value stored into either
// is checked where it is stored. ADR 0016 says which data from an error a
// message or a panel answer may carry. The gate runs the check over the
// module's production code (.github/scripts/gate.sh).
//
// The check reads one package at a time. It follows a call into another
// package of the module by what that package's check recorded: whether each
// result of the function carries data from an error, and which parameters
// it is built from. It judges a value of a struct error type the module
// defines by what is stored in it. A store in the package being checked is
// reported at its line. Another package's stores are recorded with the
// type, so a value that carries an error's data from them is reported where
// it reaches a person. A call into a package outside the module counts as
// built from all its arguments, and an error type from outside the module,
// or one that isn't a struct, counts as built from an error on sight.
package main

import (
	"go/token"
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/analysis/singlechecker"
	"golang.org/x/tools/go/ssa"
)

var analyzer = &analysis.Analyzer{
	Name:      "errorreply",
	Doc:       "reports a message to Discord or a panel answer that carries data from an error",
	Requires:  []*analysis.Analyzer{buildssa.Analyzer},
	Run:       run,
	FactTypes: []analysis.Fact{new(results), new(contents)},
}

func main() { singlechecker.Main(analyzer) }

// module is the path of the module whose code the check reads. A package
// outside it, the standard library or a dependency, is read through its
// types alone, so a call into one counts as built from all its arguments.
const module = "github.com/7cav/cavbot2"

// results is what each result of a function of the module is built from,
// recorded by the check of the function's package for the checks of the
// packages that call it.
type results struct {
	Of []result
}

func (*results) AFact() {}

// result is what one result of a function is built from: data from an
// error, or the parameters at Params, counting a method's receiver first.
type result struct {
	Carries bool
	Params  []int
}

// contents is whether the package that defines an error type stores data
// from an error in it, recorded by that package's check for the checks of
// the packages that read a value of the type.
type contents struct {
	Carries bool
}

func (*contents) AFact() {}

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

// The panel's package, whose calls that answer a browser are panel answers.
// Another package of the module that answers a browser, such as the smoke
// tool's fake forum, answers a maintainer, not a panel user.
const panelPackage = module + "/panel"

// The calls that answer a browser, by their SSA names, and the position of
// the argument that carries the answer, counting a method's receiver first.
var panelAnswers = map[string]int{
	"net/http.Error":                            1,
	"net/http.Redirect":                         2,
	"(*html/template.Template).Execute":         2,
	"(*html/template.Template).ExecuteTemplate": 3,
}

// The fields of commands' types whose value reaches a panel page through the
// store or the temp VC runtime, where the check can't follow it, by type and
// field name.
var panelFields = map[string]bool{
	"github.com/7cav/cavbot2/commands.ReportMember.Failure": true,
	"github.com/7cav/cavbot2/commands.SpawnFailure.Cause":   true,
}

// The diagnostics, one for each place a value is handed on to.
const (
	toDiscord = "this message to Discord carries data from an error; send fixed text and log the error or capture it (ADR 0016)"
	toPanel   = "this panel answer carries data from an error; answer with fixed text and log the error or capture it (ADR 0016)"
)

var errorType = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

func run(pass *analysis.Pass) (any, error) {
	if path := pass.Pkg.Path(); path != module && !strings.HasPrefix(path, module+"/") {
		return nil, nil
	}
	funcs := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA).SrcFuncs
	flow(pass, funcs, true, map[*types.TypeName]string{}, func(pos token.Pos, diag string) {
		pass.Reportf(pos, "%s", diag)
	})
	exportContents(pass, funcs)
	exportResults(pass, funcs)
	return nil, nil
}

// flow traces each value funcs hand on toward a person and reports each one
// built from an error. With sinks set, a value is handed on by a sink call
// or a store into a panel field. Either way a value stored into one of the
// reached error types is handed on, since such a value carries what is
// stored in it.
//
// A function that passes one of its parameters on to a sink is a sink for
// that parameter too, and once a value of an error type the module defines
// reaches a sink, each store into that type is checked. Each round over the
// package finds the wrappers and the types the sinks known so far reach,
// until a round finds none. flow returns the wrappers.
func flow(pass *analysis.Pass, funcs []*ssa.Function, sinks bool, reached map[*types.TypeName]string, report func(token.Pos, string)) map[*ssa.Function]map[int]string {
	wrappers := map[*ssa.Function]map[int]string{}
	reported := map[token.Pos]bool{}
	for found := true; found; {
		found = false
		for _, fn := range funcs {
			for _, block := range fn.Blocks {
				for _, instr := range block.Instrs {
					for _, out := range outgoing(instr, sinks, wrappers, reached) {
						t := &tracer{pass: pass, seen: map[visit]bool{}}
						if t.trace(out.v, nil) && !reported[instr.Pos()] {
							reported[instr.Pos()] = true
							report(instr.Pos(), out.diag)
						}
						for _, forwarded := range t.forwarded {
							// A closure can pass on a parameter of the
							// function that encloses it, which makes that
							// function the wrapper.
							owner := forwarded.Parent()
							index := slices.Index(owner.Params, forwarded)
							if wrappers[owner] == nil {
								wrappers[owner] = map[int]string{}
							}
							if _, ok := wrappers[owner][index]; !ok {
								wrappers[owner][index] = out.diag
								found = true
							}
						}
						for _, typ := range t.reached {
							if _, ok := reached[typ]; !ok {
								reached[typ] = out.diag
								found = true
							}
						}
					}
				}
			}
		}
	}
	return wrappers
}

// exportContents records, for each struct error type the package defines,
// whether a store in the package puts data from an error in it, for the
// checks of the packages that read a value of the type.
func exportContents(pass *analysis.Pass, funcs []*ssa.Function) {
	scope := pass.Pkg.Scope()
	for _, name := range scope.Names() {
		obj, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || !isStruct(obj) || !isError(obj.Type()) {
			continue
		}
		fact := &contents{}
		flow(pass, funcs, false, map[*types.TypeName]string{obj: ""}, func(token.Pos, string) {
			fact.Carries = true
		})
		pass.ExportObjectFact(obj, fact)
	}
}

// isError reports whether typ, or a pointer to it, is an error.
func isError(typ types.Type) bool {
	return types.Implements(typ, errorType) || types.Implements(types.NewPointer(typ), errorType)
}

// exportResults records what each result of each function of the package is
// built from, for the checks of the packages that call it.
func exportResults(pass *analysis.Pass, funcs []*ssa.Function) {
	type export struct {
		obj  *types.Func
		fact *results
	}
	// Each fact is exported once all are found, so that finding one never
	// reads another this package recorded.
	var exports []export
	for _, fn := range funcs {
		obj, ok := fn.Object().(*types.Func)
		if !ok {
			continue
		}
		fact := &results{Of: make([]result, fn.Signature.Results().Len())}
		for i := range fact.Of {
			t := &tracer{pass: pass, seen: map[visit]bool{}, through: true}
			fact.Of[i].Carries = t.traceReturns(fn, nil, i)
			for _, param := range t.forwarded {
				if index := slices.Index(fn.Params, param); index >= 0 && !slices.Contains(fact.Of[i].Params, index) {
					fact.Of[i].Params = append(fact.Of[i].Params, index)
				}
			}
		}
		exports = append(exports, export{obj, fact})
	}
	for _, e := range exports {
		pass.ExportObjectFact(e.obj, e.fact)
	}
}

// sent is a value an instruction hands on toward a person, and the
// diagnostic that names where it goes.
type sent struct {
	v    ssa.Value
	diag string
}

// outgoing returns the values instr hands on toward a person: the arguments
// of a sink call that carry the message or the answer, or the value stored
// into a field of an error type that reaches a sink.
func outgoing(instr ssa.Instruction, sinks bool, wrappers map[*ssa.Function]map[int]string, reached map[*types.TypeName]string) []sent {
	switch instr := instr.(type) {
	case ssa.CallInstruction:
		var values []sent
		answers := instr.Parent().Pkg.Pkg.Path() == panelPackage
		for param, diag := range sinkParams(instr.Common(), sinks, answers, wrappers) {
			values = append(values, sent{instr.Common().Args[param], diag})
		}
		return values
	case *ssa.Store:
		if field, ok := instr.Addr.(*ssa.FieldAddr); ok && sinks && panelFields[fieldName(field)] {
			return []sent{{instr.Val, toPanel}}
		}
		for addr := instr.Addr; ; {
			field, ok := addr.(*ssa.FieldAddr)
			if !ok {
				return nil
			}
			if diag, ok := reached[typeName(field.X.Type())]; ok {
				return []sent{{instr.Val, diag}}
			}
			addr = field.X
		}
	}
	return nil
}

// fieldName returns the field field addresses, named by its package path,
// its type's name and its own name, or "" when its type has no name.
func fieldName(field *ssa.FieldAddr) string {
	obj := typeName(field.X.Type())
	if obj == nil {
		return ""
	}
	if st, ok := obj.Type().Underlying().(*types.Struct); ok {
		return obj.Pkg().Path() + "." + obj.Name() + "." + st.Field(field.Field).Name()
	}
	return ""
}

// sinkParams returns the positions of call's arguments that reach a person,
// each with the diagnostic that names where it goes.
func sinkParams(call *ssa.CallCommon, sinks, answers bool, wrappers map[*ssa.Function]map[int]string) map[int]string {
	if !sinks {
		if callee := call.StaticCallee(); callee != nil {
			return wrappers[callee]
		}
		return nil
	}
	if call.IsInvoke() {
		if param, ok := sinkMethods[call.Method.Name()]; ok && len(call.Args) > param {
			return map[int]string{param: toDiscord}
		}
		return nil
	}
	callee := call.StaticCallee()
	if callee == nil {
		return nil
	}
	if param, ok := sinkMethods[callee.Name()]; ok && isSessionMethod(callee) && len(call.Args) > param+1 {
		// The receiver is the call's first argument.
		return map[int]string{param + 1: toDiscord}
	}
	if callee.Name() == sinkName && callee.Pkg != nil && callee.Pkg.Pkg.Path() == sinkPackage && len(call.Args) > sinkParam {
		return map[int]string{sinkParam: toDiscord}
	}
	if param, ok := panelAnswers[callee.String()]; ok && answers && len(call.Args) > param {
		return map[int]string{param: toPanel}
	}
	return wrappers[callee]
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
// than a time and a value of an error type the module defines, which is
// judged by what was stored in it (ADR 0016).
// It follows a call into a function of the package being checked, through
// its return values and back out through its parameters to the call's
// arguments, and a call into another package of the module by what that
// package's check recorded about it. A call into any other function counts
// as built from all its arguments.
type tracer struct {
	pass *analysis.Pass
	seen map[visit]bool
	// forwarded holds the parameters of the traced function, or of a
	// function enclosing it, that the value was built from.
	forwarded []*ssa.Parameter
	// reached holds the error types the module defines that the value was
	// built from. Their stores carry the value's data.
	reached []*types.TypeName
	// through, set while recording a function's results for the checks of
	// other packages, traces a value of an error type the package defines
	// on to what was stored in it. A caller in another package that gets
	// the value as an error reads only the result's fact, not the type's.
	through bool
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
		obj := typeName(v.Type())
		var fact contents
		switch {
		case obj != nil && isStruct(obj) && obj.Pkg() == t.pass.Pkg:
			if !t.through {
				t.reached = append(t.reached, obj)
				return false
			}
		case obj != nil && isStruct(obj) && t.pass.ImportObjectFact(obj, &fact):
			// An error type another package of the module defines carries
			// what that package stores in it, and what this one does.
			t.reached = append(t.reached, obj)
			return fact.Carries
		case !t.followable(v, f):
			return true
		}
	}
	switch v := v.(type) {
	case *ssa.Call:
		if carries, followed := t.traceCall(v, -1, f); followed {
			return carries
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
			if carries, followed := t.traceCall(call, v.Index, f); followed {
				return carries
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
	case *ssa.ChangeType:
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
// call into the module. Any other error value carries its error's data.
func (t *tracer) followable(v ssa.Value, f *frame) bool {
	switch v := v.(type) {
	case *ssa.Const, *ssa.MakeInterface, *ssa.ChangeInterface, *ssa.Phi:
		return true
	case *ssa.Call:
		return t.follows(v, f)
	case *ssa.Extract:
		call, ok := v.Tuple.(*ssa.Call)
		return ok && t.follows(call, f)
	case *ssa.Parameter:
		return f != nil && f.call.Call.StaticCallee() == v.Parent()
	}
	return false
}

// follows reports whether the tracer can follow call: into the body of a
// function of the package being checked, or by what the check of another
// package of the module recorded about the function.
func (t *tracer) follows(call *ssa.Call, f *frame) bool {
	return t.callee(call, f) != nil || t.results(call) != nil
}

// traceCall traces the result of call at index, or every result when index
// is negative, when the tracer follows call. followed reports whether it
// does.
func (t *tracer) traceCall(call *ssa.Call, index int, f *frame) (carries, followed bool) {
	if callee := t.callee(call, f); callee != nil {
		return t.traceReturns(callee, &frame{call: call, parent: f}, index), true
	}
	if fact := t.results(call); fact != nil {
		return t.traceResults(call, fact, index, f), true
	}
	return false, false
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

// results returns what the check of another package of the module recorded
// about the function call enters, or nil when it recorded nothing.
func (t *tracer) results(call *ssa.Call) *results {
	callee := call.Call.StaticCallee()
	if callee == nil || callee.Object() == nil {
		return nil
	}
	obj := callee.Object().(*types.Func)
	// A method's facts count its receiver as the first parameter, so they
	// fit a call that passes the receiver as its first argument, and not a
	// call to the method bound to a receiver.
	params := obj.Signature().Params().Len()
	if obj.Signature().Recv() != nil {
		params++
	}
	var fact results
	if len(call.Call.Args) != params || !t.pass.ImportObjectFact(obj, &fact) {
		return nil
	}
	return &fact
}

// traceResults traces the result at index of a call into another package of
// the module, or every result when index is negative, by what that
// package's check recorded about it.
func (t *tracer) traceResults(call *ssa.Call, fact *results, index int, f *frame) bool {
	for i, r := range fact.Of {
		if index >= 0 && i != index {
			continue
		}
		if r.Carries {
			return true
		}
		for _, param := range r.Params {
			if param < len(call.Call.Args) && t.trace(call.Call.Args[param], f) {
				return true
			}
		}
	}
	return false
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
