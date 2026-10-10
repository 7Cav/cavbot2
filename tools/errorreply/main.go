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
// result of the function carries data from an error, which parameters it is
// built from, and which it hands on, itself or through the functions it
// calls, to a message, a panel answer, an error type that reaches one, or
// another package's variable. A call that passes such a parameter data from
// an error is reported where it's made, with the diagnostic the function's
// own package gives it. It judges a value of a struct error type the module
// defines by what is stored in it. A store in the package being checked is
// reported at its line. Another package's stores are recorded with the type,
// so a value that carries an error's data from them is reported where it
// reaches a person. A call into a package outside the module counts as built
// from all its arguments, and an error type from outside the module, or one
// that isn't a struct, counts as built from an error on sight.
//
// A value read out of a slice, array, map, channel or interface counts as
// built from everything put in it: an element read by index or by range, a
// map's key or value, a type assertion, a receive. So does the whole
// container. The index or key it is read with, a range's position in a
// string, and the second result of a comma-ok read carry none of it.
//
// A closure is read as part of the function that makes it. What the closure
// puts in a variable it captured counts as put there, whoever calls the
// closure, and so does what it puts in a channel, map, slice or pointer it
// loads out of that variable. A parameter of the enclosing function that the
// closure reads is a parameter of that function. The closure's own parameter
// is built from what the enclosing function passes it at each call it makes.
// A local counts as built from everything written through another local that
// holds its address, even after that local points elsewhere.
//
// A package-level variable counts as built from everything its package puts
// in it: its declared value, a store at an address within it, a map update
// or a send on it, a call it's passed to, and what each call in the package
// passes a function that puts its parameter there. Another package of the module that reads the variable
// gets the verdict its package recorded. The check can't follow a value
// that one package puts in another's variable to where the variable is
// read, so it reports that store where it's made, and a call that passes a
// value to another package's function that puts it in a variable.
package main

import (
	"go/token"
	"go/types"
	"maps"
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
	FactTypes: []analysis.Fact{new(funcFact), new(typeFact), new(varFact)},
}

func main() { singlechecker.Main(analyzer) }

// module is the path of the module whose code the check reads. A package
// outside it, the standard library or a dependency, is read through its
// types alone, so a call into one counts as built from all its arguments.
const module = "github.com/7cav/cavbot2"

// funcFact is what the check of a function's package records about the
// function for the checks of the packages that call it: what each of its
// results is built from, which of its parameters it stores into an error
// type the package defines, which, counting a method's receiver first, it
// puts in a package-level variable of the package, and which it hands on
// to a message, a panel answer, an error type that reaches one, or another
// package's variable, with the diagnostic for each.
type funcFact struct {
	Results    []result
	Stores     []store
	VarParams  []int
	SinkParams map[int]string
}

func (*funcFact) AFact() {}

// result is what one result of a function is built from: data from an
// error, or the parameters at Params, counting a method's receiver first.
type result struct {
	Carries bool
	Params  []int
}

// store is a parameter of a function, counting a method's receiver first,
// that the function stores into the error type of its package named Type.
type store struct {
	Param int
	Type  string
}

// typeFact is what the check of an error type's package records about the
// type for the checks of the packages that read a value of it: whether a
// store in its package puts data from an error in it.
type typeFact struct {
	Carries bool
}

func (*typeFact) AFact() {}

// varFact is what the check of a package-level variable's package records
// about the variable for the checks of the packages that read it: whether
// what its package puts in it carries data from an error.
type varFact struct {
	Carries bool
}

func (*varFact) AFact() {}

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
	toDiscord  = "this message to Discord carries data from an error; send fixed text and log the error or capture it (ADR 0016)"
	toPanel    = "this panel answer carries data from an error; answer with fixed text and log the error or capture it (ADR 0016)"
	toVariable = "this puts data from an error in another package's variable, where the check can't follow it to a person; put fixed text there and log the error or capture it (ADR 0016)"
)

var errorType = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

func run(pass *analysis.Pass) (any, error) {
	if !inModule(pass.Pkg) {
		return nil, nil
	}
	src := newSource(pass)
	wrappers := flow(&sinks{src: src, reached: map[*types.TypeName]string{}}, func(pos token.Pos, diag string) {
		pass.Reportf(pos, "%s", diag)
	})
	stores := exportTypes(src)
	varParams := exportVars(src)
	exportFuncs(src, stores, varParams, wrappers)
	return nil, nil
}

// inModule reports whether pkg is a package of the module.
func inModule(pkg *types.Package) bool {
	return pkg.Path() == module || strings.HasPrefix(pkg.Path(), module+"/")
}

// source is the code of the package being checked: its functions, the
// values they put in each of its package-level variables, and the calls
// they make to each function with a body.
type source struct {
	pass   *analysis.Pass
	pkg    *ssa.Package
	funcs  []*ssa.Function
	writes map[*ssa.Global][]ssa.Value
	calls  map[*ssa.Function][]ssa.CallInstruction
}

// newSource reads the package pass checks. Its functions take in the
// package initializer, which buildssa leaves out of the package's source
// functions and which holds each variable's declared value.
func newSource(pass *analysis.Pass) *source {
	built := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	src := &source{pass: pass, pkg: built.Pkg, funcs: built.SrcFuncs, writes: map[*ssa.Global][]ssa.Value{}, calls: map[*ssa.Function][]ssa.CallInstruction{}}
	if init := built.Pkg.Func("init"); init != nil {
		src.funcs = append(src.funcs, init)
	}
	for _, fn := range src.funcs {
		for _, block := range fn.Blocks {
			for _, instr := range block.Instrs {
				for _, p := range puts(instr) {
					if g := variable(p.into); g != nil && g.Pkg == built.Pkg {
						src.writes[g] = append(src.writes[g], p.values...)
					}
				}
				if call, ok := instr.(ssa.CallInstruction); ok {
					if callee := call.Common().StaticCallee(); callee != nil {
						src.calls[callee] = append(src.calls[callee], call)
					}
				}
			}
		}
	}
	return src
}

// tracer returns a tracer of values in the package. through is as the
// tracer's field says.
func (src *source) tracer(through bool) *tracer {
	return &tracer{src: src, seen: map[visit]bool{}, through: through}
}

// putting is a container an instruction puts values in, and those values.
type putting struct {
	into   ssa.Value
	values []ssa.Value
}

// puts returns each container instr puts values in: the address a store
// writes, the map a map update sets, the channel a send sends on, and each
// pointer, slice, map or channel a call is passed, since the call may write
// its other arguments there, as a strings.Builder's WriteString does. A
// call's argument loaded from a variable is left out. It is a logger or a
// client kept there, more often than not, and what a call passes it isn't
// kept in the variable.
func puts(instr ssa.Instruction) []putting {
	switch instr := instr.(type) {
	case *ssa.Store:
		return []putting{{instr.Addr, []ssa.Value{instr.Val}}}
	case *ssa.MapUpdate:
		return []putting{{instr.Map, []ssa.Value{instr.Key, instr.Value}}}
	case *ssa.Send:
		return []putting{{instr.Chan, []ssa.Value{instr.X}}}
	case ssa.CallInstruction:
		var out []putting
		args := instr.Common().Args
		for i, arg := range args {
			if _, loaded := arg.(*ssa.UnOp); isContainer(arg.Type()) && !loaded {
				out = append(out, putting{arg, slices.Delete(slices.Clone(args), i, i+1)})
			}
		}
		return out
	}
	return nil
}

// isContainer reports whether a value of typ shares what it holds with its
// copies: a pointer, slice, map or channel.
func isContainer(typ types.Type) bool {
	switch typ.Underlying().(type) {
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan:
		return true
	}
	return false
}

// within returns the container v is an address or a slice within, or nil.
func within(v ssa.Value) ssa.Value {
	switch v := v.(type) {
	case *ssa.IndexAddr:
		return v.X
	case *ssa.FieldAddr:
		return v.X
	case *ssa.Slice:
		return v.X
	}
	return nil
}

// variable returns the package-level variable v is, or is within, or is
// loaded from, or nil when v is none of those.
func variable(v ssa.Value) *ssa.Global {
	for v != nil {
		if g, ok := v.(*ssa.Global); ok {
			return g
		}
		if load, ok := v.(*ssa.UnOp); ok && load.Op == token.MUL {
			v = load.X
		} else {
			v = within(v)
		}
	}
	return nil
}

// flow traces each value the package's functions hand on to s and reports
// each one built from an error.
//
// A function that passes one of its parameters on to a sink is a sink for
// that parameter too, and once a value of an error type the module defines
// reaches a sink, each store into that type is checked. Each round over the
// package finds the wrappers and the types the sinks known so far reach,
// until a round finds none. flow returns the wrappers.
func flow(s *sinks, report func(token.Pos, string)) map[*ssa.Function]map[int]string {
	src, reached := s.src, s.reached
	s.wrappers = map[*ssa.Function]map[int]string{}
	reported := map[token.Pos]bool{}
	for found := true; found; {
		found = false
		for _, fn := range src.funcs {
			for _, block := range fn.Blocks {
				for _, instr := range block.Instrs {
					for _, out := range s.outgoing(instr) {
						t := src.tracer(false)
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
							if s.wrappers[owner] == nil {
								s.wrappers[owner] = map[int]string{}
							}
							if _, ok := s.wrappers[owner][index]; !ok {
								s.wrappers[owner][index] = out.diag
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
	return s.wrappers
}

// exportTypes records, for each struct error type the package defines,
// whether a store in the package puts data from an error in it, for the
// checks of the packages that read a value of the type. It returns, for
// each function, the parameters it stores into one of those types.
func exportTypes(src *source) map[*ssa.Function][]store {
	stores := map[*ssa.Function][]store{}
	scope := src.pass.Pkg.Scope()
	for _, name := range scope.Names() {
		obj, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || !isStruct(obj) || !isError(obj.Type()) {
			continue
		}
		fact := &typeFact{}
		wrappers := flow(&sinks{src: src, storesOnly: true, reached: map[*types.TypeName]string{obj: ""}}, func(token.Pos, string) {
			fact.Carries = true
		})
		src.pass.ExportObjectFact(obj, fact)
		for fn, params := range wrappers {
			for param := range params {
				stores[fn] = append(stores[fn], store{Param: param, Type: name})
			}
		}
	}
	return stores
}

// exportVars records, for each package-level variable of the package,
// whether what the package puts in it carries data from an error, for the
// checks of the packages that read it. It returns, for each function, the
// parameters it puts in one of the variables.
func exportVars(src *source) map[*ssa.Function][]int {
	for _, member := range src.pkg.Members {
		g, ok := member.(*ssa.Global)
		if !ok || g.Object() == nil {
			continue
		}
		src.pass.ExportObjectFact(g.Object(), &varFact{Carries: src.tracer(true).traceVariable(g)})
	}
	varParams := map[*ssa.Function][]int{}
	wrappers := flow(&sinks{src: src, storesOnly: true, ownVars: true, reached: map[*types.TypeName]string{}}, func(token.Pos, string) {})
	for fn, params := range wrappers {
		varParams[fn] = slices.Sorted(maps.Keys(params))
	}
	return varParams
}

// isError reports whether typ, or a pointer to it, is an error.
func isError(typ types.Type) bool {
	return types.Implements(typ, errorType) || types.Implements(types.NewPointer(typ), errorType)
}

// exportFuncs records, for each function of the package, what each of its
// results is built from, the parameters it stores into the package's error
// types or puts in its variables, and the parameters wrappers holds for it,
// for the checks of the packages that call it.
func exportFuncs(src *source, stores map[*ssa.Function][]store, varParams map[*ssa.Function][]int, wrappers map[*ssa.Function]map[int]string) {
	type export struct {
		obj  *types.Func
		fact *funcFact
	}
	// Each fact is exported once all are found, so that finding one never
	// reads another this package recorded.
	var exports []export
	for _, fn := range src.funcs {
		obj, ok := fn.Object().(*types.Func)
		if !ok {
			continue
		}
		fact := &funcFact{Results: make([]result, fn.Signature.Results().Len()), Stores: stores[fn], VarParams: varParams[fn], SinkParams: wrappers[fn]}
		for i := range fact.Results {
			t := src.tracer(true)
			fact.Results[i].Carries = t.traceReturns(fn, nil, i)
			for _, param := range t.forwarded {
				if index := slices.Index(fn.Params, param); index >= 0 && !slices.Contains(fact.Results[i].Params, index) {
					fact.Results[i].Params = append(fact.Results[i].Params, index)
				}
			}
		}
		exports = append(exports, export{obj, fact})
	}
	for _, e := range exports {
		src.pass.ExportObjectFact(e.obj, e.fact)
	}
}

// sinks is what one flow over a package hands a value on to. Unless
// storesOnly is set, that is a checked call, a store into a panel field,
// and a value put in another package's variable. Either way a value stored
// into one of the reached error types is handed on, since such a value
// carries what is stored in it.
type sinks struct {
	src *source
	// storesOnly leaves out the checked calls, fields and other packages'
	// variables, so that only a store into a reached type, or a call that
	// passes a value on to one, hands a value on.
	storesOnly bool
	// ownVars hands on each value put in a package-level variable of the
	// package, so that the flow finds the functions that put a parameter
	// in one.
	ownVars bool
	// wrappers holds the functions of the package that pass a parameter on
	// to a sink, and the diagnostic for each such parameter.
	wrappers map[*ssa.Function]map[int]string
	// reached holds the error types of the module that reach a sink, and
	// the diagnostic for a store into each.
	reached map[*types.TypeName]string
}

// sent is a value an instruction hands on toward a person, and the
// diagnostic that names where it goes.
type sent struct {
	v    ssa.Value
	diag string
}

// outgoing returns the values instr hands on toward a person: the arguments
// of a sink call that carry the message or the answer, the value stored
// into a panel field or into a field of an error type that reaches a sink,
// or the values put in a package-level variable that s hands on.
func (s *sinks) outgoing(instr ssa.Instruction) []sent {
	values := s.variables(instr)
	switch instr := instr.(type) {
	case ssa.CallInstruction:
		answers := instr.Parent().Pkg.Pkg.Path() == panelPackage
		for param, diag := range s.params(instr.Common(), answers) {
			values = append(values, sent{instr.Common().Args[param], diag})
		}
	case *ssa.Store:
		if diag, ok := s.field(instr.Addr); ok {
			values = append(values, sent{instr.Val, diag})
		}
	}
	return values
}

// variables returns the values instr puts in a package-level variable that
// s hands on: one of the package's own when ownVars is set, or else one of
// another package of the module, whose check can't follow the value to
// where the variable is read.
func (s *sinks) variables(instr ssa.Instruction) []sent {
	var out []sent
	for _, p := range puts(instr) {
		g := variable(p.into)
		if g == nil {
			continue
		}
		var diag string
		switch {
		case s.ownVars && g.Pkg == s.src.pkg:
			// This flow only finds the functions that put a parameter in
			// a variable, and reports nothing, so no diagnostic is needed.
		case !s.storesOnly && g.Pkg != s.src.pkg && inModule(g.Pkg.Pkg):
			diag = toVariable
		default:
			continue
		}
		for _, v := range p.values {
			out = append(out, sent{v, diag})
		}
	}
	return out
}

// field returns the diagnostic for a value stored at addr into a panel
// field, or into a field of an error type that reaches a sink.
func (s *sinks) field(addr ssa.Value) (string, bool) {
	if field, ok := addr.(*ssa.FieldAddr); ok && !s.storesOnly && panelFields[fieldName(field)] {
		return toPanel, true
	}
	for {
		field, ok := addr.(*ssa.FieldAddr)
		if !ok {
			return "", false
		}
		if diag, ok := s.reached[typeName(field.X.Type())]; ok {
			return diag, true
		}
		addr = field.X
	}
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

// params returns the positions of call's arguments that reach a person,
// each with the diagnostic that names where it goes. answers says whether
// call is made in the panel's package.
func (s *sinks) params(call *ssa.CallCommon, answers bool) map[int]string {
	if !s.storesOnly {
		if params := checkedParams(call, answers); params != nil {
			return params
		}
	}
	callee := call.StaticCallee()
	params := maps.Clone(s.wrappers[callee])
	if params == nil {
		params = map[int]string{}
	}
	if fact := importFuncFact(s.src.pass, call); fact != nil {
		// A function of another package of the module that stores a
		// parameter into one of its error types passes that parameter on
		// to a sink once the type reaches one.
		for _, st := range fact.Stores {
			obj, _ := callee.Object().Pkg().Scope().Lookup(st.Type).(*types.TypeName)
			if diag, ok := s.reached[obj]; ok {
				params[st.Param] = diag
			}
		}
		// One that puts a parameter in a package-level variable puts it
		// where this check can't follow it.
		if !s.storesOnly {
			for _, param := range fact.VarParams {
				params[param] = toVariable
			}
			// One whose check found it handing a parameter on to a sink is
			// a sink for that parameter too, with the same diagnostic.
			maps.Copy(params, fact.SinkParams)
		}
	}
	return params
}

// checkedParams returns the positions of the arguments of call that carry a
// message to Discord or, when answers is set, a panel answer, each with the
// diagnostic that names where it goes, or nil when call is no checked call.
func checkedParams(call *ssa.CallCommon, answers bool) map[int]string {
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
	return nil
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
// as built from all its arguments. It follows a read out of a container to
// what was put in it, and a package-level variable to what its package puts
// in it.
type tracer struct {
	src  *source
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

// visit is a value traced inside the chain of frames f the tracer entered to
// reach it.
type visit struct {
	v ssa.Value
	f *frame
}

// frame is a function body the tracer followed a value into: the callee of
// a call, or a closure it reached through a variable the closure puts values
// in, which runs at each call of the closure.
type frame struct {
	fn *ssa.Function
	// call is the call that entered fn, or nil for a closure reached through
	// a variable.
	call *ssa.Call
	// parent is the frame call is made in, or the frame a closure's
	// enclosing function runs in. A closure's calls are made there too.
	parent *frame
}

// anywhere is the frame of a value the tracer reached through a
// package-level variable. Any call of the function that stores the value
// may run the store, so a parameter there is built from what each call in
// the package passes it.
var anywhere = &frame{}

func (f *frame) entered(fn *ssa.Function) bool {
	for ; f != nil && f != anywhere; f = f.parent {
		if f.fn == fn {
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
		var fact typeFact
		switch {
		case obj != nil && isStruct(obj) && obj.Pkg() == t.src.pass.Pkg:
			if !t.through {
				t.reached = append(t.reached, obj)
				return false
			}
		case obj != nil && isStruct(obj) && t.src.pass.ImportObjectFact(obj, &fact):
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
		return t.traceCall(v, -1, f)
	case *ssa.Extract:
		switch tuple := v.Tuple.(type) {
		case *ssa.Call:
			return t.traceCall(tuple, v.Index, f)
		case *ssa.Next:
			// A range step's results are whether it got an element, the
			// element's key and its value. The key of a range over a string
			// is a position in it, and carries none of the string.
			if iter, ok := tuple.Iter.(*ssa.Range); ok && (v.Index == 2 || v.Index == 1 && !tuple.IsString) {
				return t.trace(iter.X, f)
			}
		// The second result of a comma-ok lookup, type assertion or receive
		// says whether it got a value, and carries none of it.
		case *ssa.Lookup:
			return v.Index == 0 && t.trace(tuple.X, f)
		case *ssa.TypeAssert:
			return v.Index == 0 && t.trace(tuple.X, f)
		case *ssa.UnOp:
			return v.Index == 0 && t.trace(tuple.X, f)
		case *ssa.Select:
			// A select's first two results say which case it took and
			// whether a receive got a value. The rest are what its receive
			// cases got, in order.
			recv := v.Index - 2
			for _, state := range tuple.States {
				if state.Dir != types.RecvOnly {
					continue
				}
				if recv == 0 {
					return t.trace(state.Chan, f)
				}
				recv--
			}
		}
	case *ssa.Parameter:
		switch {
		case f == nil:
			t.forwarded = append(t.forwarded, v)
		case f == anywhere:
			return t.traceCallers(v, anywhere)
		case f.fn != v.Parent():
		case f.call != nil:
			return t.trace(f.call.Call.Args[slices.Index(v.Parent().Params, v)], f.parent)
		default:
			// A closure reached through a variable runs at each call of it,
			// which its enclosing function makes.
			return t.traceCallers(v, f.parent)
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
	case *ssa.Alloc, *ssa.MakeSlice, *ssa.MakeMap, *ssa.MakeChan:
		return t.traceStores(v, f)
	case *ssa.FreeVar:
		return t.traceBindings(v, f)
	case *ssa.UnOp:
		return t.trace(v.X, f)
	case *ssa.FieldAddr:
		return t.trace(v.X, f)
	case *ssa.Field:
		return t.trace(v.X, f)
	case *ssa.IndexAddr:
		return t.trace(v.X, f)
	case *ssa.Index:
		return t.trace(v.X, f)
	case *ssa.Lookup:
		return t.trace(v.X, f)
	case *ssa.TypeAssert:
		return t.trace(v.X, f)
	case *ssa.Global:
		return t.traceVariable(v)
	}
	return false
}

// traceVariable traces every value the package puts in the package-level
// variable g, or reads what the check of g's package recorded about it.
// What a variable holds doesn't depend on the call the tracer reached it in.
func (t *tracer) traceVariable(g *ssa.Global) bool {
	if g.Pkg != t.src.pkg {
		var fact varFact
		return g.Object() != nil && t.src.pass.ImportObjectFact(g.Object(), &fact) && fact.Carries
	}
	for _, v := range t.src.writes[g] {
		if t.trace(v, anywhere) {
			return true
		}
	}
	return false
}

// traceCallers traces what each call in the package passes the parameter p,
// inside the frame f the calls are made in.
func (t *tracer) traceCallers(p *ssa.Parameter, f *frame) bool {
	index := slices.Index(p.Parent().Params, p)
	for _, call := range t.src.calls[p.Parent()] {
		if args := call.Common().Args; index < len(args) && t.trace(args[index], f) {
			return true
		}
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
		callee, fact := t.follow(v, f)
		return callee != nil || fact != nil
	case *ssa.Extract:
		call, ok := v.Tuple.(*ssa.Call)
		if !ok {
			return false
		}
		callee, fact := t.follow(call, f)
		return callee != nil || fact != nil
	case *ssa.Parameter:
		return f != nil && f != anywhere && f.call != nil && f.fn == v.Parent()
	}
	return false
}

// follow returns what the tracer follows call by: the body of a function
// of the package being checked, or else what the check of another package
// of the module recorded about the function. Both are nil when the tracer
// can't follow call.
func (t *tracer) follow(call *ssa.Call, f *frame) (*ssa.Function, *funcFact) {
	if callee := t.callee(call, f); callee != nil {
		return callee, nil
	}
	return nil, importFuncFact(t.src.pass, &call.Call)
}

// traceCall traces the result of call at index, or every result when index
// is negative. When the tracer can't follow call, each of its results counts
// as built from all its arguments and the interface value a method call is
// made on.
func (t *tracer) traceCall(call *ssa.Call, index int, f *frame) bool {
	switch callee, fact := t.follow(call, f); {
	case callee != nil:
		return t.traceReturns(callee, &frame{fn: callee, call: call, parent: f}, index)
	case fact != nil:
		return t.traceResults(call, fact, index, f)
	}
	if call.Call.IsInvoke() && t.trace(call.Call.Value, f) {
		return true
	}
	for _, arg := range call.Call.Args {
		if t.trace(arg, f) {
			return true
		}
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

// importFuncFact returns what the check of another package of the module
// recorded about the function call enters, or nil when it recorded nothing.
func importFuncFact(pass *analysis.Pass, call *ssa.CallCommon) *funcFact {
	callee := call.StaticCallee()
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
	var fact funcFact
	if len(call.Args) != params || !pass.ImportObjectFact(obj, &fact) {
		return nil
	}
	return &fact
}

// traceResults traces the result at index of a call into another package of
// the module, or every result when index is negative, by what that
// package's check recorded about it.
func (t *tracer) traceResults(call *ssa.Call, fact *funcFact, index int, f *frame) bool {
	for i, r := range fact.Results {
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
// to the free variable v, in the frame the enclosing function runs in.
func (t *tracer) traceBindings(v *ssa.FreeVar, f *frame) bool {
	closure := v.Parent()
	index := slices.Index(closure.FreeVars, v)
	if f != nil && f != anywhere && f.fn == closure {
		f = f.parent
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

// traceStores traces every value put in addr, or in an address or a slice
// within it, as puts lists them, whether the function puts it there, a
// closure it captures addr in, a container it loads out of addr, or another
// local that holds addr.
func (t *tracer) traceStores(addr ssa.Value, f *frame) bool {
	for _, ref := range *addr.Referrers() {
		for _, p := range puts(ref) {
			if p.into != addr {
				continue
			}
			for _, v := range p.values {
				if t.trace(v, f) {
					return true
				}
			}
		}
		if v, ok := ref.(ssa.Value); ok && within(v) == addr && t.traceStores(v, f) {
			return true
		}
		// A channel, map, slice or pointer loaded out of addr shares what
		// is put in it with the one addr holds.
		if load, ok := ref.(*ssa.UnOp); ok && load.Op == token.MUL && isContainer(load.Type()) && t.traceStores(load, f) {
			return true
		}
		// A local that holds addr puts in it what is written through it.
		if store, ok := ref.(*ssa.Store); ok && store.Val == addr {
			if holder, ok := store.Addr.(*ssa.Alloc); ok && t.trace(holder, f) {
				return true
			}
		}
		// A closure that captures addr puts values in it through its free
		// variable.
		if mk, ok := ref.(*ssa.MakeClosure); ok {
			closure := mk.Fn.(*ssa.Function)
			for i, binding := range mk.Bindings {
				if binding == addr && t.traceStores(closure.FreeVars[i], &frame{fn: closure, parent: f}) {
					return true
				}
			}
		}
	}
	return false
}
