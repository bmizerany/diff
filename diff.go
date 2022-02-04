package diff

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"unsafe"
)

// Each compares values a and b, calling f for each difference it finds.
// By default, its conditions for equality are similar to reflect.DeepEqual.
//
//   diff.Each(fmt.Printf, a, b)
//
// The behavior can be adjusted by supplying Option values.
// See Default for a complete list of default options.
// Values in opt apply in addition to (and override) the defaults.
func Each(f func(format string, arg ...any), a, b any, opt ...Option) {
	each(func() {}, f, a, b, opt...)
}

// Log compares values a and b, calling out.Output for each difference
// it finds.
// By default, its conditions for equality are similar to reflect.DeepEqual.
//
//   diff.Log(log.Default(), a, b)
//
// Log provides a calldepth argument to out.Output to show the file
// and line number of the call to Log. This is usually preferable to
// passing log.Printf to Each.
//
// The behavior can be adjusted by supplying Option values.
// See Default for a complete list of default options.
// Values in opt apply in addition to (and override) the defaults.
func Log(out Outputter, a, b any, opt ...Option) {
	depth := stackDepth()
	f := func(format string, arg ...any) {
		dd := stackDepth() - depth
		out.Output(dd+2, fmt.Sprintf(format, arg...))
	}
	each(func() {}, f, a, b, opt...)
}

// Test compares values a and b, calling f for each difference it finds.
// By default, its conditions for equality are similar to reflect.DeepEqual.
//
//   diff.Test(t, t.Errorf, a, b)
//   diff.Test(t, t.Fatalf, a, b)
//   diff.Test(t, t.Logf, a, b)
//
// Test also calls h.Helper() at the top of every internal function.
// Note that *testing.T and *testing.B satisfy this interface.
// This makes test output show the file and line number of the call to
// Test.
//
// The behavior can be adjusted by supplying Option values.
// See Default for a complete list of default options.
// Values in opt apply in addition to (and override) the defaults.
func Test(h Helperer, f func(format string, arg ...any), a, b any, opt ...Option) {
	h.Helper()
	each(h.Helper, f, a, b, opt...)
}

func each(h func(), f func(format string, arg ...any), a, b any, opt ...Option) {
	h()
	d := &differ{
		aSeen: map[visit]visit{},
		bSeen: map[visit]visit{},
	}
	d.config.helper = h
	d.config.xform = map[reflect.Type]reflect.Value{}
	d.config.format = map[reflect.Type]reflect.Value{}
	OptionList(defaultOpt, OptionList(opt...)).apply(&d.config)
	e := &printEmitter{sink: f, level: d.config.level, helper: h}
	d.walk(e, reflect.ValueOf(a), reflect.ValueOf(b), true, true)
}

type Helperer interface {
	Helper()
}

type Outputter interface {
	Output(calldepth int, s string) error
}

type differ struct {
	config config
	aSeen  map[visit]visit
	bSeen  map[visit]visit
}

type config struct {
	level level // verbosity

	// equalFuncs treats non-nil functions as equal.
	// In the == operator, non-nil function values
	// are never equal, so it is often useless to compare them.
	equalFuncs bool

	// xform transforms values of the given type before
	// they are included in the diff tree.
	// hashes, weights, and differences are computed
	// using the transformed values.
	xform map[reflect.Type]reflect.Value

	format map[reflect.Type]reflect.Value

	helper func()
}

type visit struct {
	p unsafe.Pointer
	t reflect.Type
}

type emitfer interface {
	emitf(av, bv reflect.Value, format string, arg ...any)
	subf(format string, arg ...any) emitfer
	didEmit() bool
}

type printEmitter struct {
	level  level
	helper func()
	path   []string
	did    bool
	sink   func(format string, a ...any)
}

func (e *printEmitter) emitf(av, bv reflect.Value, format string, arg ...any) {
	e.helper()
	e.did = true
	var p string
	if len(e.path) > 0 {
		p = strings.Join(e.path, "") + ": "
	}
	switch e.level {
	case auto:
		arg = append([]any{p}, arg...)
		e.sink("%s"+format+"\n", arg...)
	case pathOnly:
		e.sink("%s\n", strings.Join(e.path, ""))
	case full:
		e.sink("%s%#v != %#v\n", p, av, bv)
	default:
		panic("diff: bad verbose level")
	}
}

func (e *printEmitter) subf(format string, arg ...any) emitfer {
	return &printEmitter{
		level:  e.level,
		helper: e.helper,
		path:   append(e.path, fmt.Sprintf(format, arg...)),
		did:    false,
		sink: func(format string, a ...any) {
			e.helper()
			e.did = true
			e.sink(format, a...)
		},
	}
}

func (e *printEmitter) didEmit() bool {
	return e.did
}

type countEmitter struct {
	n int
}

func (e *countEmitter) emitf(av, bv reflect.Value, format string, arg ...any) {
	e.n++
}

func (e *countEmitter) subf(format string, arg ...any) emitfer {
	return e
}

func (e *countEmitter) didEmit() bool {
	return e.n > 0
}

func reflectApply(f reflect.Value, v ...reflect.Value) reflect.Value {
	return f.Call(v)[0]
}

func (d *differ) equal(av, bv reflect.Value) bool {
	d2 := &differ{
		config: d.config,
		aSeen:  map[visit]visit{},
		bSeen:  map[visit]visit{},
	}
	d2.config.xform = nil
	d2.config.format = nil
	e := &countEmitter{}
	d2.walk(e, av, bv, true, true)
	return !e.didEmit()
}

func (d *differ) walk(e emitfer, av, bv reflect.Value, xformOk, wantType bool) {
	d.config.helper()
	if !av.IsValid() && !bv.IsValid() {
		return
	}
	if !av.IsValid() {
		e.emitf(av, bv, "nil != %v", formatShort(bv, true))
		return
	}
	if !bv.IsValid() {
		e.emitf(av, bv, "%v != nil", formatShort(av, true))
		return
	}

	t := av.Type()
	if bt := bv.Type(); t != bt {
		e.emitf(av, bv, "%v != %v", t, bt)
		return
	}

	// Check for cycles.
	switch t.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice:
		if av.IsNil() || bv.IsNil() {
			break
		}
		avis := visit{unsafe.Pointer(av.Pointer()), t}
		bvis := visit{unsafe.Pointer(bv.Pointer()), t}
		if bSeen, ok := d.aSeen[avis]; ok {
			if bSeen != bvis {
				e.emitf(av, bv, "uneven cycle")
			}
			return
		}
		if _, ok := d.bSeen[bvis]; ok {
			e.emitf(av, bv, "uneven cycle")
			return
		}
		d.aSeen[avis] = bvis
		d.bSeen[bvis] = avis
	}

	// Check for a transform func.
	var ax, bx reflect.Value
	var haveXform bool
	if xformOk {
		var xf reflect.Value
		xf, haveXform = d.config.xform[t]
		if haveXform && xformOk {
			ax = reflectApply(xf, av)
			bx = reflectApply(xf, bv)
			if d.equal(ax, bx) {
				return
			}
		}
	}

	// Check for a format func.
	if ff, ok := d.config.format[t]; ok && !d.equal(av, bv) {
		s := reflectApply(ff, av, bv).String()
		e.emitf(av, bv, "%s", s)
		return
	}

	// We use almost the same rules as reflect.DeepEqual here,
	// but with a couple of configuration options that modify
	// the behavior, such as:
	//   * We allow the client to ignore functions.
	//   * We allow the client to ignore unexported fields.
	// See "go doc reflect DeepEqual" for more.
	switch t.Kind() {
	case reflect.Array:
		// TODO(kr): fancy diff (histogram, myers)
		for i := 0; i < t.Len(); i++ {
			d.walk(e.subf("[%d]", i), av.Index(i), bv.Index(i), true, false)
		}
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			d.walk(e.subf("."+t.Field(i).Name), av.Field(i), bv.Field(i), true, false)
		}
	case reflect.Func:
		if d.config.equalFuncs {
			break
		}
		if !av.IsNil() || !bv.IsNil() {
			d.emitPointers(e, av, bv, wantType)
		}
	case reflect.Interface:
		d.walk(e, av.Elem(), bv.Elem(), true, true)
	case reflect.Map:
		if av.IsNil() != bv.IsNil() {
			d.emitPointers(e, av, bv, wantType)
			break
		}
		if av.Pointer() == bv.Pointer() {
			break
		}
		ak, both, bk := keyDiff(av, bv)
		for _, k := range ak {
			e.subf("[%#v]", k).
				emitf(av.MapIndex(k), bv.MapIndex(k), "(removed)")
		}
		for _, k := range both {
			d.walk(e.subf("[%#v]", k), av.MapIndex(k), bv.MapIndex(k), true, false)
		}
		for _, k := range bk {
			e.subf("[%#v]", k).
				emitf(av.MapIndex(k), bv.MapIndex(k), "(added) %v", formatShort(bv.MapIndex(k), false))
		}
	case reflect.Ptr:
		if av.Pointer() == bv.Pointer() {
			break
		}
		if av.IsNil() != bv.IsNil() {
			e.emitf(av, bv, "%v != %v", formatShort(av, wantType), formatShort(bv, wantType))
			break
		}
		d.walk(e, av.Elem(), bv.Elem(), true, wantType)
	case reflect.Slice:
		if av.IsNil() != bv.IsNil() {
			d.emitPointers(e, av, bv, wantType)
			break
		}
		if av.Len() == bv.Len() && av.Pointer() == bv.Pointer() {
			break
		}
		// TODO(kr): fancy diff (histogram, myers)
		n := av.Len()
		if blen := bv.Len(); n != blen {
			e.emitf(av, bv, "{len %d} != {len %d}", n, blen)
			return
		}
		for i := 0; i < n; i++ {
			d.walk(e.subf("[%d]", i), av.Index(i), bv.Index(i), true, false)
		}
	case reflect.Bool:
		d.eqtest(e, av, bv, av.Bool(), bv.Bool(), wantType)
	case reflect.Int, reflect.Int8, reflect.Int16,
		reflect.Int32, reflect.Int64:
		d.eqtest(e, av, bv, av.Int(), bv.Int(), wantType)
	case reflect.Uint, reflect.Uint8, reflect.Uint16,
		reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		d.eqtest(e, av, bv, av.Uint(), bv.Uint(), wantType)
	case reflect.Float32, reflect.Float64:
		d.eqtest(e, av, bv, av.Float(), bv.Float(), wantType)
	case reflect.Complex64, reflect.Complex128:
		d.eqtest(e, av, bv, av.Complex(), bv.Complex(), wantType)
	case reflect.String:
		if a, b := av.String(), bv.String(); a != b {
			e.emitf(av, bv, "%q != %q", a, b)
		}
	case reflect.Chan, reflect.UnsafePointer:
		if a, b := av.Pointer(), bv.Pointer(); a != b {
			d.emitPointers(e, av, bv, wantType)
		}
	default:
		panic("diff: unknown reflect.Kind " + t.Kind().String())
	}

	// The xform check returns early if the transformed values are
	// deeply equal. So if we got this far, we know they are different.
	// If we didn't find a difference in the untransformed values, make
	// sure to emit *something*, and then diff the *transformed* values.
	if haveXform && !e.didEmit() {
		e.emitf(av, bv, "(transformed values differ)")
		d.walk(e.subf("->"), ax, bx, false, true)
	}
}

func (d *differ) eqtest(e emitfer, av, bv reflect.Value, a, b any, wantType bool) {
	d.config.helper()
	if a != b {
		e.emitf(av, bv, "%v != %v",
			formatShort(av, wantType),
			formatShort(bv, wantType),
		)
	}
}

func (d *differ) emitPointers(e emitfer, av, bv reflect.Value, wantType bool) {
	d.config.helper()
	e.emitf(av, bv, "%v != %v",
		formatShort(av, wantType),
		formatShort(bv, wantType),
	)
}

func keyDiff(av, bv reflect.Value) (ak, both, bk []reflect.Value) {
	for aIter := av.MapRange(); aIter.Next(); {
		k := aIter.Key()
		if !bv.MapIndex(k).IsValid() {
			ak = append(ak, k)
		} else {
			both = append(both, k)
		}
	}
	for bIter := bv.MapRange(); bIter.Next(); {
		k := bIter.Key()
		if !av.MapIndex(k).IsValid() {
			bk = append(bk, k)
		}
	}
	return ak, both, bk
}

func stackDepth() int {
	pc := make([]uintptr, 1000)
	return runtime.Callers(0, pc)
}
