package cli

import (
	"fmt"
	"reflect"
	"strconv"
	"teleparse/internal/filters"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// filterFlags binds filters.Options to cobra flags and remembers the command.
type filterFlags struct {
	opts filters.Options
	cmd  *cobra.Command
}

// triBoolValue implements pflag.Value for filters.TriBool.
type triBoolValue struct{ tri *filters.TriBool }

func (v triBoolValue) String() string { return v.tri.String() }
func (triBoolValue) Type() string     { return "tristate" }

func (v triBoolValue) Set(text string) error {
	value, err := strconv.ParseBool(text)
	if err != nil {
		return fmt.Errorf("%q: %w: %w", text, filters.ErrBadBool, err)
	}

	v.tri.Set(value)

	return nil
}

// textValue adapts a pointer-to-T (T implements UnmarshalText) to pflag.Value.
type textValue struct{ ptr reflect.Value }

// textUnmarshaler is the interface a textValue target must implement.
type textUnmarshaler interface{ UnmarshalText(text []byte) error }

func (v textValue) String() string {
	if v.ptr.IsNil() {
		return ""
	}

	out := v.ptr.MethodByName("String").Call(nil)
	if text, ok := out[0].Interface().(string); ok {
		return text
	}

	return ""
}

func (textValue) Type() string { return "text" }

func (v textValue) Set(text string) error {
	fresh := reflect.New(v.ptr.Type().Elem())
	unmarshaler, ok := fresh.Interface().(textUnmarshaler)

	if !ok {
		return fmt.Errorf("%s: %w", v.ptr.Type().Elem(), filters.ErrNotUnmarshaler)
	}

	if err := unmarshaler.UnmarshalText([]byte(text)); err != nil {
		return fmt.Errorf("set flag value: %w", err)
	}

	v.ptr.Set(fresh)

	return nil
}

func triBoolType() reflect.Type { return reflect.TypeOf(filters.TriBool{}) }
func textUnmarshalerType() reflect.Type {
	return reflect.TypeOf((*textUnmarshaler)(nil)).Elem()
}

// mustCast asserts v to T at flag-registration time; failure is a programmer error.
func mustCast[T any](v any) T { //nolint:ireturn // generic cast helper returns T by design
	cast, ok := v.(T)
	if !ok {
		panic(fmt.Sprintf("filters: flag field cast failed for %T", v))
	}

	return cast
}

// registerReflectFlags walks opts (pointer to struct) and registers one flag
// per field carrying a `flag` tag. Supported kinds: string, bool, int, int64,
// float64, slice-of-string, filters.TriBool, and nested structs (recursed).
// A field without a tag is skipped (the Recursion parent struct relies on this).
func registerReflectFlags(flagSet *pflag.FlagSet, opts any) {
	structVal := reflect.ValueOf(opts).Elem()
	structType := structVal.Type()

	for i := range structType.NumField() {
		field := structType.Field(i)
		tag := field.Tag.Get("flag")

		if tag == "" {
			continue
		}

		usage := field.Tag.Get("usage")
		fieldVal := structVal.Field(i)
		addr := fieldVal.Addr().Interface()

		switch {
		case field.Type == triBoolType():
			registerTriBool(flagSet, tag, usage, fieldVal)
		case field.Type.Kind() == reflect.Pointer && field.Type.Elem().Implements(textUnmarshalerType()):
			flagSet.Var(textValue{ptr: fieldVal}, tag, usage)
		case field.Type.Kind() == reflect.String:
			flagSet.StringVar(mustCast[*string](addr), tag, "", usage)
		case field.Type.Kind() == reflect.Bool:
			flagSet.BoolVar(mustCast[*bool](addr), tag, false, usage)
		case field.Type.Kind() == reflect.Int:
			flagSet.IntVar(mustCast[*int](addr), tag, 0, usage)
		case field.Type.Kind() == reflect.Int64:
			flagSet.Int64Var(mustCast[*int64](addr), tag, 0, usage)
		case field.Type.Kind() == reflect.Float64:
			flagSet.Float64Var(mustCast[*float64](addr), tag, 0, usage)
		case field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.String:
			flagSet.StringSliceVar(mustCast[*[]string](addr), tag, nil, usage)
		case field.Type.Kind() == reflect.Struct:
			registerReflectFlags(flagSet, addr)
		default:
			panic(fmt.Sprintf("filters: unhandled field %s (%s)", field.Name, field.Type))
		}
	}
}

func registerTriBool(flagSet *pflag.FlagSet, tag, usage string, fieldVal reflect.Value) {
	tri, ok := fieldVal.Addr().Interface().(*filters.TriBool)
	if !ok {
		panic("filters: TriBool field assertion failed for " + tag)
	}

	flagSet.Var(triBoolValue{tri: tri}, tag, usage+" (true|false)")

	flagSet.Lookup(tag).NoOptDefVal = "true"
}
