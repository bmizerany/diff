package diff

import (
	"fmt"
	"io"
	"reflect"
	"text/tabwriter"
	"unsafe"

	"kr.dev/diff/internal/indent"
)

var reflectAny = reflect.TypeOf((*any)(nil)).Elem()

func formatShort(v reflect.Value, wantType bool) fmt.Formatter {
	return &formatter{
		root:       v,
		wantType:   wantType,
		full:       false,
		allowDepth: 2,
	}
}

func formatFull(v reflect.Value) fmt.Formatter {
	return &formatter{
		root:       v,
		wantType:   true,
		full:       true,
		allowDepth: 1e8,
	}
}

type formatter struct {
	root       reflect.Value
	wantType   bool
	full       bool
	allowDepth int
}

func (f *formatter) Format(fs fmt.State, verb rune) {
	var w io.Writer = fs
	if f.full {
		w = indent.New(w, []byte("    "))
	}
	f.writeTo(w, f.root, f.wantType, 1)
}

func (f *formatter) writeTo(w io.Writer, v reflect.Value, wantType bool, depth int) {
	// TODO(kr): detect recursion
	if !v.IsValid() {
		io.WriteString(w, "nil") // untyped nil
		return
	}
	switch t := v.Type(); t.Kind() {
	case reflect.Array:
		if wantType {
			writeType(w, t)
		}
		if depth >= f.allowDepth && t.Len() > 0 {
			io.WriteString(w, "{...}")
			break
		}
		io.WriteString(w, "{")
		if f.full && t.Len() > 1 {
			io.WriteString(w, "\n")
			ww := indent.New(w, []byte("    "))
			for i := 0; i < t.Len(); i++ {
				f.writeTo(ww, v.Index(i), false, depth+1)
				io.WriteString(ww, ",\n")
			}
		} else {
			for i := 0; i < t.Len(); i++ {
				if i > 0 {
					io.WriteString(w, ", ...")
					break
				}
				f.writeTo(w, v.Index(i), false, depth+1)
			}
		}
		io.WriteString(w, "}")
	case reflect.Struct:
		if wantType {
			writeType(w, t)
		}
		if depth >= f.allowDepth && t.NumField() > 0 {
			io.WriteString(w, "{...}")
			break
		}
		io.WriteString(w, "{")
		if f.full && t.NumField() > 1 {
			io.WriteString(w, "\n")
			tw := tabwriter.NewWriter(w, 0, 8, 1, ' ', 0)
			ww := indent.New(tw, []byte("    "))
			for i := 0; i < t.NumField(); i++ {
				io.WriteString(ww, t.Field(i).Name)
				io.WriteString(ww, ":\t")
				f.writeTo(ww, v.Field(i), false, depth+1)
				io.WriteString(ww, ",\n")
			}
			tw.Flush()
		} else {
			for i := 0; i < t.NumField(); i++ {
				if i > 0 {
					io.WriteString(w, ", ...")
					break
				}
				io.WriteString(w, t.Field(i).Name)
				io.WriteString(w, ":")
				f.writeTo(w, v.Field(i), false, depth+1)
			}
		}
		io.WriteString(w, "}")
	case reflect.Func:
		if v.IsNil() {
			writeTypedNil(w, t, wantType)
			break
		}
		fmt.Fprintf(w, "%v {...}", t)
	case reflect.Interface:
		f.writeTo(w, v.Elem(), true, depth)
	case reflect.Map:
		if v.IsNil() {
			writeTypedNil(w, t, wantType)
			break
		}
		if wantType {
			writeType(w, t)
		}
		if depth >= f.allowDepth && v.Len() > 0 {
			io.WriteString(w, "{...}")
			break
		}
		io.WriteString(w, "{")

		if f.full && v.Len() > 1 {
			io.WriteString(w, "\n")
			tw := tabwriter.NewWriter(w, 0, 8, 1, ' ', 0)
			ww := indent.New(tw, []byte("    "))
			for _, mk := range sortedKeys(v) {
				mv := v.MapIndex(mk)
				f.writeTo(ww, mk, false, 0)
				io.WriteString(ww, ":\t")
				f.writeTo(ww, mv, false, depth+1)
				io.WriteString(ww, ",\n")
			}
			tw.Flush()
		} else {
			first := true
			for _, mk := range sortedKeys(v) {
				if !first {
					io.WriteString(w, ", ...")
					break
				}
				first = false
				mv := v.MapIndex(mk)
				f.writeTo(w, mk, false, 0)
				io.WriteString(w, ":")
				f.writeTo(w, mv, false, depth+1)
			}
		}

		io.WriteString(w, "}")
	case reflect.Ptr:
		if v.IsNil() {
			writeTypedNil(w, t, wantType)
			break
		}
		if wantType || t.Elem().Kind() != reflect.Struct {
			io.WriteString(w, "&")
		}
		if t.Elem().Kind() == reflect.Pointer {
			// Two or more pointers in a row is confusing,
			// so show the type to be extra explicit.
			wantType = true
		}
		f.writeTo(w, v.Elem(), wantType, depth) // note: don't increment depth
	case reflect.Slice:
		if v.IsNil() {
			writeTypedNil(w, t, wantType)
			break
		}
		if wantType {
			writeType(w, t)
		}
		if depth >= f.allowDepth && v.Len() > 0 {
			io.WriteString(w, "{...}")
			break
		}
		io.WriteString(w, "{")

		if f.full && v.Len() > 1 {
			io.WriteString(w, "\n")
			ww := indent.New(w, []byte("    "))
			for i := 0; i < v.Len(); i++ {
				f.writeTo(ww, v.Index(i), false, depth+1)
				io.WriteString(ww, ",\n")
			}
		} else {
			for i := 0; i < v.Len(); i++ {
				if i > 0 {
					io.WriteString(w, ", ")
					if !f.full {
						io.WriteString(w, "...")
						break
					}
				}
				f.writeTo(w, v.Index(i), false, depth+1)
			}
		}
		io.WriteString(w, "}")
	case reflect.Bool:
		writeSimple(w, "%v", v, wantType && t.PkgPath() != "")
	case reflect.Int, reflect.Int8, reflect.Int16,
		reflect.Int32, reflect.Int64:
		writeSimple(w, "%v", v, wantType)
	case reflect.Uint, reflect.Uint8, reflect.Uint16,
		reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		writeSimple(w, "%v", v, wantType)
	case reflect.Float32, reflect.Float64:
		writeSimple(w, "%v", v, wantType)
	case reflect.Complex64, reflect.Complex128:
		writeSimple(w, "%v", v, wantType)
	case reflect.String:
		// TODO(kr): abbreviate
		writeSimple(w, "%q", v, wantType && t.PkgPath() != "")
	case reflect.Chan:
		if v.IsNil() {
			writeTypedNil(w, t, wantType)
			break
		}
		io.WriteString(w, "(")
		writeType(w, t)
		io.WriteString(w, ")")
		fmt.Fprintf(w, "(%p)", unsafe.Pointer(v.Pointer()))
	case reflect.UnsafePointer:
		fmt.Fprintf(w, "unsafe.Pointer(%p)", unsafe.Pointer(v.Pointer()))
	default:
		w.Write([]byte("(unknown kind)"))
	}
}

func writeSimple(w io.Writer, verb string, v reflect.Value, showType bool) {
	if showType {
		writeType(w, v.Type())
		io.WriteString(w, "(")
	}
	fmt.Fprintf(w, verb, v)
	if showType {
		io.WriteString(w, ")")
	}
}

func writeTypedNil(w io.Writer, t reflect.Type, showType bool) {
	// TODO(kr): print type name here sometimes (depending on context)
	if showType {
		needParens := false
		switch t.Kind() {
		case reflect.Func, reflect.Pointer, reflect.Chan:
			needParens = t.Name() == ""
		}
		if needParens {
			io.WriteString(w, "(")
		}
		writeType(w, t)
		if needParens {
			io.WriteString(w, ")")
		}
		io.WriteString(w, "(")
	}
	io.WriteString(w, "nil")
	if showType {
		io.WriteString(w, ")")
	}
}

func writeType(w io.Writer, t reflect.Type) {
	if t == reflectAny {
		io.WriteString(w, "any")
		return
	}

	if name := t.Name(); name != "" {
		io.WriteString(w, t.String())
		return
	}

	switch t.Kind() {
	case reflect.Array:
		fmt.Fprintf(w, "[%d]", t.Len())
		writeType(w, t.Elem())
	case reflect.Struct:
		io.WriteString(w, "struct{")
		if t.NumField() > 0 {
			io.WriteString(w, " ")
		}
		for i := 0; i < t.NumField(); i++ {
			if i > 0 {
				io.WriteString(w, "; ")
			}
			field := t.Field(i)
			io.WriteString(w, field.Name)
			io.WriteString(w, " ")
			writeType(w, field.Type)
		}
		if t.NumField() > 0 {
			io.WriteString(w, " ")
		}
		io.WriteString(w, "}")
	case reflect.Func:
		io.WriteString(w, "func")
		writeFunc(w, t)
	case reflect.Interface:
		io.WriteString(w, "interface{ ")
		for i := 0; i < t.NumMethod(); i++ {
			if i > 0 {
				io.WriteString(w, "; ")
			}
			method := t.Method(i)
			io.WriteString(w, method.Name)
			writeFunc(w, method.Type)
		}
		io.WriteString(w, " }")
	case reflect.Map:
		io.WriteString(w, "map[")
		writeType(w, t.Key())
		io.WriteString(w, "]")
		writeType(w, t.Elem())
	case reflect.Ptr:
		io.WriteString(w, "*")
		writeType(w, t.Elem())
	case reflect.Slice:
		io.WriteString(w, "[]")
		writeType(w, t.Elem())
	case reflect.Chan:
		if t.ChanDir() == reflect.RecvDir {
			io.WriteString(w, "<-")
		}
		io.WriteString(w, "chan")
		if t.ChanDir() == reflect.SendDir {
			io.WriteString(w, "<-")
		}
		io.WriteString(w, " ")
		writeType(w, t.Elem())
	default:
		fmt.Fprint(w, t)
	}
}

func writeFunc(w io.Writer, f reflect.Type) {
	io.WriteString(w, "(")
	n := f.NumIn()
	for i := 0; i < n; i++ {
		if i > 0 {
			io.WriteString(w, ", ")
		}
		if i == n-1 && f.IsVariadic() {
			io.WriteString(w, "...")
			writeType(w, f.In(i).Elem())
		} else {
			writeType(w, f.In(i))
		}
	}
	io.WriteString(w, ")")
	n = f.NumOut()
	if n > 0 {
		io.WriteString(w, " ")
	}
	if n > 1 {
		io.WriteString(w, "(")
	}
	for i := 0; i < n; i++ {
		if i > 0 {
			io.WriteString(w, ", ")
		}
		writeType(w, f.Out(i))
	}
	if n > 1 {
		io.WriteString(w, ")")
	}
}
