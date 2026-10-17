// Command errorreply reports where the bot's code takes data out of an error
// and hands it to anything but the log, Sentry, fmt.Errorf, a panic or a
// comparison (ADR 0018). That keeps an error's text, status code and
// response body out of every reply, channel post and panel answer
// (ADR 0016), without following where a reply's data came from.
//
// The check reads data leaving an error in these ways:
//
//   - a call of one of its methods, a method value or a method expression
//   - a read of one of its fields
//   - a conversion of it, or of a value holding it in an exported field, an
//     element or a pointer, to an interface other than error, including a
//     type assertion and an errors.As target that isn't an error
//   - recover
//   - a call into code the check doesn't read that is handed one: a call
//     outside the module passes when its signature can return and write
//     nothing but bools, times and errors, as errors.Is's does, and a call
//     through an interface or a function value when it returns nothing else
//   - a generic function instantiated with one for a type parameter that
//     may not be an error
//
// Methods and fields this module declares aren't ways out: what an error
// type of the module holds was checked where it was put there. A type of
// the module whose Error method returns data from another error counts as
// an error from outside the module, so its text may be logged but not
// replied with.
//
// Data that has left an error passes when every use of it, followed through
// the function, is a comparison, a call that can only answer yes or no about
// it, such as strings.Contains, crypto/sha256's Sum256, the log or Sentry,
// fmt.Errorf, or a panic. It may pass through local variables, containers
// the function made, closures, and functions of the module whose parameter
// reaches only those, which the check works out for each function and hands
// to the packages that call it. Any other use is reported where the data
// left the error. A bool, a time, a duration and an error are clean, so data
// that becomes one stops there.
//
// The log is log/slog and Sentry is sentry-go, and what a call into either
// returns carries what it was handed. Three things point the log or Sentry
// at code or a place the check doesn't read, and each is reported where it
// happens: a value of another package's type converted to an interface
// either declares, a function handed to either other than a scope
// configurator, and a writer other than os.Stdout and os.Stderr handed to
// log/slog. The check accepts one such value, the voice package's
// errorsAsWarnings, a slog.Handler that moves disgo's ERRORs down to WARN
// and hands each record on. It accepts it by name, without reading what it
// does with a record (ADR 0018).
//
// Other ways past the check exist, such as fmt printing an unexported field
// by reflection. Such a way counts as a bug only when the bot's code takes
// it (ADR 0018).
package main

import (
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/analysis/singlechecker"
	"golang.org/x/tools/go/ssa"
)

var analyzer = &analysis.Analyzer{
	Name:      "errorreply",
	Doc:       "reports where data leaves an error for anywhere but the log, Sentry, fmt.Errorf, a panic or a comparison",
	Requires:  []*analysis.Analyzer{buildssa.Analyzer},
	Run:       run,
	FactTypes: []analysis.Fact{new(logParams), new(wrapsError)},
}

func main() { singlechecker.Main(analyzer) }

// logParams marks which parameters of a function of the module reach only
// the log or Sentry, so data from an error may be handed to them.
type logParams struct {
	Params []bool
}

func (*logParams) AFact() {}

func (f *logParams) String() string {
	var b strings.Builder
	for _, p := range f.Params {
		if p {
			b.WriteByte('L')
		} else {
			b.WriteByte('-')
		}
	}
	return "logParams(" + b.String() + ")"
}

// wrapsError marks an error type of the module whose Error method returns
// data from another error, so the check reads it as it reads an error from
// outside the module.
type wrapsError struct{}

func (*wrapsError) AFact() {}

func (*wrapsError) String() string { return "wrapsError" }

var errorType = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

// isError reports whether a value of t is an error: t, or a pointer to t,
// has an Error method.
func isError(t types.Type) bool {
	if types.Implements(t, errorType) {
		return true
	}
	if types.IsInterface(t) {
		return false
	}
	if _, ok := t.Underlying().(*types.Pointer); ok {
		return false
	}
	return types.Implements(types.NewPointer(t), errorType)
}

// clean reports whether a value of t carries none of an error's data that a
// person could read: a bool, a time or a duration (ADR 0016), or an error
// itself, which the check reads again wherever data leaves it.
func clean(t types.Type) bool {
	if b, ok := t.Underlying().(*types.Basic); ok && b.Kind() == types.Bool {
		return true
	}
	if n, ok := types.Unalias(t).(*types.Named); ok && (declared(n.Obj(), "time", "Time") || declared(n.Obj(), "time", "Duration")) {
		return true
	}
	return types.Implements(t, errorType)
}

// declared reports whether obj is the declaration called name in the package
// at path.
func declared(obj types.Object, path, name string) bool {
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == path && obj.Name() == name
}

// held is the type of what v holds: the field or element an address points
// at, or else v's own type.
func held(v ssa.Value) types.Type {
	switch v := v.(type) {
	case *ssa.FieldAddr, *ssa.IndexAddr:
		return v.Type().Underlying().(*types.Pointer).Elem()
	}
	return v.Type()
}

type checker struct {
	pass *analysis.Pass
	// logParamsOf holds the parameters each function of the package being
	// checked hands only to the log or Sentry.
	logParamsOf map[*ssa.Function][]bool
	// globalUses holds the instructions that use each package-level
	// variable, which go/ssa doesn't record.
	globalUses map[*ssa.Global][]ssa.Instruction
	// wrapping holds the package's error types whose Error method returns
	// data from another error.
	wrapping map[*types.TypeName]bool
}

// walk is one walk over where data that left an error goes: the values it
// has read and, while findWrapping reads an Error method, that method and
// whether the data reaches one of its returns.
type walk struct {
	seen        map[ssa.Value]bool
	errorMethod *ssa.Function
	returned    bool
}

func newWalk() *walk { return &walk{seen: map[ssa.Value]bool{}} }

func run(pass *analysis.Pass) (any, error) {
	if !inModule(pass.Pkg.Path()) {
		return nil, nil
	}
	ssaInfo := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	c := &checker{
		pass:        pass,
		logParamsOf: map[*ssa.Function][]bool{},
		globalUses:  map[*ssa.Global][]ssa.Instruction{},
		wrapping:    map[*types.TypeName]bool{},
	}
	c.findGlobalUses(ssaInfo.Pkg, ssaInfo.SrcFuncs)
	c.summarize(ssaInfo.SrcFuncs)
	c.findWrapping(ssaInfo.SrcFuncs)
	for _, fn := range ssaInfo.SrcFuncs {
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				if redirects(instr) {
					c.pass.Reportf(posOf(instr, fn), "this points the log or Sentry at code or a place the check doesn't read, so data from an error handed to it can go anywhere (ADR 0018)")
					continue
				}
				v, ok := c.source(instr)
				if !ok {
					continue
				}
				if v != nil && c.contained(v, newWalk()) {
					continue
				}
				c.pass.Reportf(posOf(instr, fn), "data from an error leaves it here for somewhere other than the log, Sentry, fmt.Errorf, a panic or a comparison (ADR 0018)")
			}
		}
	}
	return nil, nil
}

// findGlobalUses records the instructions that use each package-level
// variable of the package, in its source functions and in the init function
// that sets the variables' declared values.
func (c *checker) findGlobalUses(pkg *ssa.Package, fns []*ssa.Function) {
	visit := func(fn *ssa.Function) {
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				for _, op := range instr.Operands(nil) {
					if g, ok := (*op).(*ssa.Global); ok {
						c.globalUses[g] = append(c.globalUses[g], instr)
					}
				}
			}
		}
	}
	init := []*ssa.Function{pkg.Func("init")}
	for len(init) > 0 {
		fn := init[0]
		init = append(init[1:], fn.AnonFuncs...)
		visit(fn)
	}
	for _, fn := range fns {
		visit(fn)
	}
}

// summarize works out which parameters of the package's functions reach only
// the log or Sentry. It starts by assuming every one does and takes each back
// that a use disproves, until a whole pass changes none, then records the
// answer for packages that call these functions.
func (c *checker) summarize(fns []*ssa.Function) {
	for _, fn := range fns {
		params := make([]bool, len(fn.Params))
		for i := range params {
			params[i] = true
		}
		c.logParamsOf[fn] = params
	}
	for changed := true; changed; {
		changed = false
		for _, fn := range fns {
			for i, p := range fn.Params {
				if c.logParamsOf[fn][i] && !c.contained(p, newWalk()) {
					c.logParamsOf[fn][i] = false
					changed = true
				}
			}
		}
	}
	for _, fn := range fns {
		if obj, ok := fn.Object().(*types.Func); ok && obj.Pkg() == c.pass.Pkg {
			c.pass.ExportObjectFact(obj, &logParams{Params: c.logParamsOf[fn]})
		}
	}
}

// findWrapping works out which of the package's error types return data
// from another error from their Error method. Once one does, a call of its
// methods takes data out of an error, which can make another type's Error
// method return some, so it repeats until a whole pass finds no more, then
// records the answer for packages that use these types.
func (c *checker) findWrapping(fns []*ssa.Function) {
	for changed := true; changed; {
		changed = false
		for _, fn := range fns {
			t := errorMethodOf(fn)
			if t == nil || t.Pkg() != c.pass.Pkg || c.wrapping[t] {
				continue
			}
			if c.returnsErrorData(fn) {
				c.wrapping[t] = true
				changed = true
			}
		}
	}
	for t := range c.wrapping {
		c.pass.ExportObjectFact(t, &wrapsError{})
	}
}

// returnsErrorData reports whether data from an error reaches a return of
// fn.
func (c *checker) returnsErrorData(fn *ssa.Function) bool {
	w := &walk{seen: map[ssa.Value]bool{}, errorMethod: fn}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if v, ok := c.source(instr); ok && v != nil {
				c.contained(v, w)
			}
		}
	}
	return w.returned
}

// errorMethodOf returns the type fn is the Error method of, when fn, a
// function of the module, is one.
func errorMethodOf(fn *ssa.Function) *types.TypeName {
	recv := fn.Signature.Recv()
	if recv == nil || fn.Name() != "Error" {
		return nil
	}
	return namedOf(recv.Type()).Obj()
}

// namedOf returns the named type t is, or points to.
func namedOf(t types.Type) *types.Named {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	n, _ := types.Unalias(t).(*types.Named)
	return n
}

// wraps reports whether t, a type of the module, returns data from another
// error from its Error method.
func (c *checker) wraps(t *types.TypeName) bool {
	if t.Pkg() == c.pass.Pkg {
		return c.wrapping[t]
	}
	return c.pass.ImportObjectFact(t, new(wrapsError))
}

// fromOutside reports whether a method or field obj of a value of type t
// gives data from an error the check reads as one from outside the module:
// obj is declared outside the module, or t wraps another error.
func (c *checker) fromOutside(obj types.Object, t types.Type) bool {
	if obj == nil || obj.Pkg() == nil || !inModule(obj.Pkg().Path()) {
		return true
	}
	n := namedOf(t)
	return n != nil && c.wraps(n.Obj())
}

// logParam reports whether fn hands its i'th parameter only to the log or
// Sentry.
func (c *checker) logParam(fn *ssa.Function, i int) bool {
	if fn.Pkg != nil && fn.Pkg.Pkg == c.pass.Pkg {
		params, ok := c.logParamsOf[fn]
		return ok && params[i]
	}
	var fact logParams
	obj, _ := fn.Object().(*types.Func)
	return obj != nil && c.pass.ImportObjectFact(obj, &fact) && i < len(fact.Params) && fact.Params[i]
}

// source reports whether instr takes data out of an error, and if so the
// value that carries it. A nil value means the data leaves where the check
// can't follow it, so instr is reported whatever comes after.
func (c *checker) source(instr ssa.Instruction) (ssa.Value, bool) {
	switch in := instr.(type) {
	case *ssa.MakeInterface:
		if types.Implements(in.Type(), errorType) || errorsAsTarget(in) {
			return nil, false
		}
		return in, c.holdsError(in.X.Type(), map[types.Type]bool{})
	case *ssa.ChangeInterface:
		return in, isError(in.X.Type()) && !types.Implements(in.Type(), errorType)
	case ssa.CallInstruction:
		common := in.Common()
		if t, ok := asTarget(common); ok && !isError(t) {
			// errors.As fills a target that isn't an error with the
			// error itself, seen through another interface, which the
			// check can't follow from here.
			return nil, true
		}
		if b, ok := common.Value.(*ssa.Builtin); ok {
			// A recovered panic's value may be an error.
			return result(in), b.Name() == "recover"
		}
		if common.IsInvoke() && isError(common.Value.Type()) {
			return result(in), true
		}
		// A call through a method value's closure isn't one: the data left
		// the error where the closure was made.
		if callee, ok := common.Value.(*ssa.Function); ok && c.errorMethod(callee) {
			return result(in), true
		}
		return nil, c.handedError(common)
	case *ssa.MakeClosure:
		return in, c.errorMethod(in.Fn.(*ssa.Function))
	case *ssa.TypeAssert:
		return in, isError(in.X.Type()) && types.IsInterface(in.AssertedType) && !types.Implements(in.AssertedType, errorType)
	case *ssa.Field:
		return in, c.leavingField(in.X.Type(), in.Field)
	case *ssa.FieldAddr:
		return in, c.leavingField(in.X.Type().Underlying().(*types.Pointer).Elem(), in.Field)
	}
	return nil, false
}

// handedError reports whether common hands an error to code the check
// doesn't read, outside the module, that could return it or write it
// somewhere: a call whose signature leaves it nothing to return or write but
// bools, times and errors, such as errors.Is, passes. So does a call into
// the module, whose code the check reads, unless it instantiates a generic
// function with an error for a type parameter that may not be one. A call
// through an interface or a function value may be the module's own code, so
// it is judged by what it returns alone: what the module's code writes is
// read where it writes it.
func (c *checker) handedError(common *ssa.CallCommon) bool {
	callee := c.callee(common)
	if callee != nil {
		if c.errorTypeArg(callee) {
			return true
		}
		if moduleFunction(callee) || sink(callee) {
			return false
		}
		if _, ok := asTarget(common); ok {
			return false
		}
	}
	for _, a := range common.Args {
		if !handsErrors(a.Type()) {
			continue
		}
		if callee == nil {
			return !cleanResults(common.Signature())
		}
		return !inertSignature(common.Signature())
	}
	return false
}

// cleanResults reports whether a function of sig returns nothing but bools,
// times and errors.
func cleanResults(sig *types.Signature) bool {
	for i := range sig.Results().Len() {
		if !clean(sig.Results().At(i).Type()) {
			return false
		}
	}
	return true
}

// errorTypeArg reports whether fn is a generic function instantiated with a
// type that holds an error for a type parameter that may not be an error,
// so the function's code, read with the type parameter, can't tell.
func (c *checker) errorTypeArg(fn *ssa.Function) bool {
	targs := fn.TypeArgs()
	if len(targs) == 0 || fn.Origin() == nil {
		return false
	}
	params := fn.Origin().Signature.TypeParams()
	if recv := fn.Origin().Signature.RecvTypeParams(); recv.Len() > 0 {
		params = recv
	}
	for i, t := range targs {
		if i < params.Len() && !types.Implements(params.At(i), errorType) && c.holdsError(t, map[types.Type]bool{}) {
			return true
		}
	}
	return false
}

// moduleFunction reports whether the check reads fn's code: fn, or the
// method a wrapper stands for, is declared in the module.
func moduleFunction(fn *ssa.Function) bool {
	if fn.Pkg != nil {
		return inModule(fn.Pkg.Pkg.Path())
	}
	obj := fn.Object()
	return obj != nil && obj.Pkg() != nil && inModule(obj.Pkg().Path())
}

// inertSignature reports whether a function of sig can return nothing but
// bools, times and errors, and takes nothing it could write into.
func inertSignature(sig *types.Signature) bool {
	if !cleanResults(sig) {
		return false
	}
	if sig.Recv() != nil && !inert(sig.Recv().Type()) {
		return false
	}
	for i := range sig.Params().Len() {
		if !inert(sig.Params().At(i).Type()) {
			return false
		}
	}
	return true
}

// holdsError reports whether a value of t, handed to fmt, a template or
// encoding/json, can show data from an error: t is an error the check reads
// as one from outside the module, or holds one in an exported field, an
// element or a pointer. Unexported fields don't count. Templates and
// encoding/json can't read one, and fmt prints one by reflection without
// calling Error, a way past the check ADR 0018 accepts.
func (c *checker) holdsError(t types.Type, seen map[types.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	if isError(t) {
		n := namedOf(t)
		if n == nil || n.Obj().Pkg() == nil || !inModule(n.Obj().Pkg().Path()) || c.wraps(n.Obj()) {
			return true
		}
	}
	switch u := t.Underlying().(type) {
	case *types.Map:
		return c.holdsError(u.Key(), seen) || c.holdsError(u.Elem(), seen)
	case interface{ Elem() types.Type }:
		// A pointer, slice, array or channel.
		return c.holdsError(u.Elem(), seen)
	case *types.Struct:
		for i := range u.NumFields() {
			f := u.Field(i)
			if f.Exported() && c.holdsError(f.Type(), seen) {
				return true
			}
		}
	}
	return false
}

// asTarget returns the type errors.As fills, when common calls errors.As.
func asTarget(common *ssa.CallCommon) (types.Type, bool) {
	callee := common.StaticCallee()
	if callee == nil || !declared(callee.Object(), "errors", "As") {
		return nil, false
	}
	if mi, ok := common.Args[1].(*ssa.MakeInterface); ok {
		if p, ok := mi.X.Type().Underlying().(*types.Pointer); ok {
			return p.Elem(), true
		}
	}
	return nil, false
}

// errorsAsTarget reports whether mi only hands errors.As the address of an
// error to fill, which carries none of the error's data.
func errorsAsTarget(mi *ssa.MakeInterface) bool {
	for _, r := range *mi.Referrers() {
		call, ok := r.(ssa.CallInstruction)
		if !ok {
			return false
		}
		t, ok := asTarget(call.Common())
		if !ok || !isError(t) {
			return false
		}
	}
	return true
}

// errorMethod reports whether fn is a method of an error that takes data out
// of it: the method itself, a method expression's thunk, or a method value's
// bound wrapper, of a method declared outside the module or of an error type
// that wraps another.
func (c *checker) errorMethod(fn *ssa.Function) bool {
	obj, ok := fn.Object().(*types.Func)
	if !ok {
		return false
	}
	recv := obj.Type().(*types.Signature).Recv()
	return recv != nil && isError(recv.Type()) && c.fromOutside(obj, recv.Type())
}

// leavingField reports whether reading the field'th field of a struct of
// type t takes data out of an error.
func (c *checker) leavingField(t types.Type, field int) bool {
	if !isError(t) {
		return false
	}
	return c.fromOutside(t.Underlying().(*types.Struct).Field(field), t)
}

// contained reports whether every use of v, a value carrying data from an
// error, keeps it to the log, Sentry or a comparison. A use the check has no
// rule for counts as one that doesn't.
func (c *checker) contained(v ssa.Value, w *walk) bool {
	if v == nil || w.seen[v] {
		return true
	}
	w.seen[v] = true
	if clean(held(v)) {
		return true
	}
	refs := v.Referrers()
	if refs == nil {
		return true
	}
	ok := true
	for _, r := range *refs {
		if !c.containedUse(v, r, w) {
			ok = false
			// findWrapping reads every use, so it finds a return a use
			// the check has no rule for would otherwise hide.
			if w.errorMethod == nil {
				return false
			}
		}
	}
	return ok
}

func (c *checker) containedUse(v ssa.Value, r ssa.Instruction, w *walk) bool {
	switch r := r.(type) {
	case *ssa.DebugRef, *ssa.Panic:
		return true
	case *ssa.Return:
		if r.Parent() == w.errorMethod {
			w.returned = true
			return true
		}
		t := errorMethodOf(r.Parent())
		return t != nil && c.wraps(t)
	case *ssa.BinOp, *ssa.Slice, *ssa.UnOp, *ssa.TypeAssert, *ssa.Extract, *ssa.MakeInterface, *ssa.ChangeInterface, *ssa.Field, *ssa.FieldAddr, *ssa.Phi, *ssa.Convert, *ssa.ChangeType:
		return c.contained(r.(ssa.Value), w)
	case *ssa.IndexAddr:
		if r.X != v {
			// An index picks an element, as a key does.
			return true
		}
		return c.contained(r, w)
	case *ssa.Lookup:
		if r.X != v {
			// A key picks a fixed message out of a table.
			return true
		}
		return c.contained(r, w)
	case *ssa.Store:
		if r.Addr == v {
			return true
		}
		root, ok := localContainer(r.Addr)
		return ok && c.contained(root, w)
	case *ssa.MapUpdate:
		if r.Map == v {
			return true
		}
		root, ok := localContainer(r.Map)
		return ok && c.contained(root, w)
	case *ssa.MakeClosure:
		fn := r.Fn.(*ssa.Function)
		for i, b := range r.Bindings {
			if b == v && !c.contained(fn.FreeVars[i], w) {
				return false
			}
		}
		return true
	case ssa.CallInstruction:
		return c.containedCall(v, r, w)
	}
	return false
}

// containedCall reports whether a call handed v keeps it to the log or
// Sentry.
func (c *checker) containedCall(v ssa.Value, call ssa.CallInstruction, w *walk) bool {
	common := call.Common()
	if common.Value == v {
		return c.contained(result(call), w)
	}
	if b, ok := common.Value.(*ssa.Builtin); ok {
		return c.containedBuiltin(v, b, call, w)
	}
	callee := c.callee(common)
	if callee == nil {
		return false
	}
	if digest(callee) {
		return true
	}
	if sink(callee) {
		// What the log or Sentry returns, such as an slog.Value, carries
		// what it was handed.
		return c.contained(result(call), w)
	}
	if callee.Pkg == nil || !inModule(callee.Pkg.Pkg.Path()) {
		return predicate(callee.Signature)
	}
	for i, a := range common.Args {
		if a == v && !c.logParam(callee, i) {
			return false
		}
	}
	return true
}

// containedBuiltin reports whether a call of a builtin handed v keeps it to
// the log, Sentry or a comparison. What append returns carries v, and so does
// the slice it appends to, since append may write into that slice's array.
func (c *checker) containedBuiltin(v ssa.Value, b *ssa.Builtin, call ssa.CallInstruction, w *walk) bool {
	args := call.Common().Args
	switch b.Name() {
	case "len", "cap":
		return c.contained(result(call), w)
	case "append":
		if args[0] != v {
			if _, ok := localSlice(args[0]); !ok || !c.contained(args[0], w) {
				return false
			}
		}
		return c.contained(result(call), w)
	}
	return false
}

// digest reports whether fn is crypto/sha256's Sum256, whose result can
// only be compared, as a time can only be read: no text of what it was
// handed survives in it.
func digest(fn *ssa.Function) bool {
	return declared(fn.Object(), "crypto/sha256", "Sum256")
}

// predicate reports whether a function of sig can only answer yes or no
// about what it is handed: it returns bools alone and takes nothing it could
// write into, so a call outside the module picks between fixed messages, as
// strings.Contains does.
func predicate(sig *types.Signature) bool {
	results := sig.Results()
	for i := range results.Len() {
		if b, ok := results.At(i).Type().Underlying().(*types.Basic); !ok || b.Kind() != types.Bool {
			return false
		}
	}
	return results.Len() > 0 && inertSignature(sig)
}

// inert reports whether a function handed a value of t can't write through
// it, or can write only errors: a string, a number, a bool, or what
// handsErrors accepts.
func inert(t types.Type) bool {
	if _, ok := t.Underlying().(*types.Basic); ok {
		return true
	}
	return handsErrors(t)
}

// handsErrors reports whether a value of t is an error, or a slice of them,
// which a variadic ...error parameter takes.
func handsErrors(t types.Type) bool {
	if s, ok := t.Underlying().(*types.Slice); ok {
		return isError(s.Elem())
	}
	return isError(t)
}

// localSlice reports whether s is a slice this function made, or nil, so
// that what append writes into it stays where the check follows it.
func localSlice(s ssa.Value) (ssa.Value, bool) {
	switch s := s.(type) {
	case *ssa.Const:
		return s, s.IsNil()
	case *ssa.Slice:
		return localContainer(s.X)
	case *ssa.MakeSlice:
		return s, true
	case *ssa.Call:
		if b, ok := s.Call.Value.(*ssa.Builtin); ok && b.Name() == "append" {
			return s, true
		}
	}
	return nil, false
}

// callee returns the function common calls, when the check can tell: its
// static callee, or the function held by a package-level variable it calls
// through.
func (c *checker) callee(common *ssa.CallCommon) *ssa.Function {
	if fn := common.StaticCallee(); fn != nil {
		return fn
	}
	return c.varFunction(common.Value)
}

// varFunction returns the function a call through v calls when v is read out
// of a package-level variable of this package that is set once, in its
// declaration, to a function, and used nowhere else but to read it, as a seam
// a test swaps is.
func (c *checker) varFunction(v ssa.Value) *ssa.Function {
	load, ok := v.(*ssa.UnOp)
	if !ok || load.Op != token.MUL {
		return nil
	}
	g, ok := load.X.(*ssa.Global)
	if !ok || g.Pkg == nil || g.Pkg.Pkg != c.pass.Pkg {
		return nil
	}
	var fn *ssa.Function
	for _, r := range c.globalUses[g] {
		switch r := r.(type) {
		case *ssa.UnOp:
			// A load, the only operation on a variable's address.
		case *ssa.Store:
			f, ok := r.Val.(*ssa.Function)
			if !ok || fn != nil || r.Parent() != g.Pkg.Func("init") {
				return nil
			}
			fn = f
		default:
			return nil
		}
	}
	return fn
}

// redirects reports whether instr points the log or Sentry away from the
// process's output and Sentry's client, where data from an error handed to
// them would go on to code or a place the check doesn't read: a value of
// another package's type converted to an interface log/slog or sentry-go
// declares, such as a slog.Handler of the module's own, other than the one
// handler the check accepts; a function handed to either package, directly
// or in a field of an options struct, other than a scope configurator; or a
// writer other than os.Stdout and os.Stderr handed to log/slog.
func redirects(instr ssa.Instruction) bool {
	switch in := instr.(type) {
	case *ssa.MakeInterface:
		return logType(in.Type()) && !logType(in.X.Type()) && !acceptedHandler(in.X.Type())
	case ssa.CallInstruction:
		callee := in.Common().StaticCallee()
		if callee == nil || !logFunction(callee) {
			return false
		}
		sig := in.Common().Signature()
		args := in.Common().Args
		if sig.Recv() != nil {
			args = args[1:]
		}
		for i, a := range args {
			if handsCode(a) {
				return true
			}
			if i < sig.Params().Len() && writer(sig.Params().At(i).Type()) && logFunction(callee) && !processOutput(a) {
				return true
			}
		}
	}
	return false
}

// acceptedHandler reports whether t is the voice package's errorsAsWarnings,
// the one slog.Handler of the module's that the check accepts, as the
// package doc says.
func acceptedHandler(t types.Type) bool {
	n := namedOf(t)
	return n != nil && declared(n.Obj(), module+"/voice", "errorsAsWarnings")
}

// logFunction reports whether fn is declared in log/slog or sentry-go.
func logFunction(fn *ssa.Function) bool {
	if o := fn.Origin(); o != nil {
		fn = o
	}
	obj := fn.Object()
	return obj != nil && obj.Pkg() != nil && logPath(obj.Pkg().Path())
}

func logPath(path string) bool {
	return path == "log/slog" || path == "github.com/getsentry/sentry-go"
}

// logType reports whether t is a type log/slog or sentry-go declares, or a
// pointer to one.
func logType(t types.Type) bool {
	n := namedOf(t)
	return n != nil && n.Obj().Pkg() != nil && logPath(n.Obj().Pkg().Path())
}

// handsCode reports whether v hands the log or Sentry code to call: a
// function, or an options struct of theirs that holds one, other than a
// scope configurator, a function of nothing but their own pointers that
// returns nothing, as sentry.WithScope takes.
func handsCode(v ssa.Value) bool {
	if sig, ok := v.Type().Underlying().(*types.Signature); ok {
		return !configurator(sig)
	}
	if load, ok := v.(*ssa.UnOp); ok && load.Op == token.MUL {
		v = load.X
	}
	alloc, ok := v.(*ssa.Alloc)
	if !ok || !logType(alloc.Type()) {
		return false
	}
	for _, r := range *alloc.Referrers() {
		field, ok := r.(*ssa.FieldAddr)
		if !ok {
			continue
		}
		for _, fr := range *field.Referrers() {
			if st, ok := fr.(*ssa.Store); ok && st.Addr == field && handsCode(st.Val) {
				return true
			}
		}
	}
	return false
}

// configurator reports whether a function of sig only configures one of the
// log's or Sentry's own values: it takes nothing but pointers to them and
// returns nothing.
func configurator(sig *types.Signature) bool {
	if sig.Results().Len() > 0 {
		return false
	}
	for i := range sig.Params().Len() {
		t := sig.Params().At(i).Type()
		if _, ok := t.Underlying().(*types.Pointer); !ok || !logType(t) {
			return false
		}
	}
	return true
}

// writer reports whether t is io.Writer.
func writer(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && declared(n.Obj(), "io", "Writer")
}

// processOutput reports whether v is os.Stdout or os.Stderr.
func processOutput(v ssa.Value) bool {
	if mi, ok := v.(*ssa.MakeInterface); ok {
		v = mi.X
	}
	load, ok := v.(*ssa.UnOp)
	if !ok {
		return false
	}
	g, ok := load.X.(*ssa.Global)
	return ok && (declared(g.Object(), "os", "Stdout") || declared(g.Object(), "os", "Stderr"))
}

// sink reports whether fn is a place data from an error may go: the log,
// Sentry, or fmt.Errorf, which keeps it inside an error.
func sink(fn *ssa.Function) bool {
	return logFunction(fn) || declared(fn.Object(), "fmt", "Errorf")
}

// module is the path of the module whose code the check reads.
const module = "github.com/7cav/cavbot2"

func inModule(path string) bool {
	return path == module || strings.HasPrefix(path, module+"/")
}

// localContainer returns the array, struct or slice made in this function
// that addr points into, if any.
func localContainer(addr ssa.Value) (ssa.Value, bool) {
	for {
		switch a := addr.(type) {
		case *ssa.Alloc, *ssa.MakeMap, *ssa.MakeSlice:
			return a, true
		case *ssa.IndexAddr:
			addr = a.X
		case *ssa.FieldAddr:
			addr = a.X
		default:
			return nil, false
		}
	}
}

// result returns what call returns, or nil for a go or defer statement,
// which returns nothing.
func result(call ssa.CallInstruction) ssa.Value {
	if v := call.Value(); v != nil {
		return v
	}
	return nil
}

// posOf is where to report instr: its own position, or else that of the
// first instruction using its value, or a use of that, that has one.
func posOf(instr ssa.Instruction, fn *ssa.Function) token.Pos {
	for in := instr; in != nil; {
		if in.Pos().IsValid() {
			return in.Pos()
		}
		v, ok := in.(ssa.Value)
		if !ok || v.Referrers() == nil || len(*v.Referrers()) == 0 {
			break
		}
		in = (*v.Referrers())[0]
	}
	return fn.Pos()
}
