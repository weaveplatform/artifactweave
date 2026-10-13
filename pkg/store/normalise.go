package store

import (
	"reflect"
	"time"
)

// Clone deep-copies v: maps, slices and pointers are duplicated, nil stays
// nil and empty stays empty, so a caller can neither see nor make later
// changes to stored state.
func Clone[V any](v V) V {
	return copyValue(reflect.ValueOf(&v).Elem(), false).Interface().(V) //nolint:forcetypeassert // same type in, same type out
}

// Normalise is Clone with times truncated to UTC microseconds and durations
// to milliseconds, the precision of the SQL columns: what every
// backend returns for a value it was given. Contract tests build their
// expectations with it.
func Normalise[V any](v V) V {
	return copyValue(reflect.ValueOf(&v).Elem(), true).Interface().(V) //nolint:forcetypeassert // same type in, same type out
}

var (
	timeType     = reflect.TypeFor[time.Time]()
	durationType = reflect.TypeFor[time.Duration]()
)

// copyValue returns a deep copy of v, normalising times and durations when
// norm is set.
func copyValue(v reflect.Value, norm bool) reflect.Value {
	out := reflect.New(v.Type()).Elem()
	switch {
	case v.Type() == timeType:
		t := v.Interface().(time.Time) //nolint:forcetypeassert // checked by type
		if norm && !t.IsZero() {
			t = t.UTC().Truncate(time.Microsecond)
		}
		out.Set(reflect.ValueOf(t))
		return out
	case v.Type() == durationType:
		d := v.Interface().(time.Duration) //nolint:forcetypeassert // checked by type
		if norm {
			d = d.Truncate(time.Millisecond)
		}
		out.Set(reflect.ValueOf(d))
		return out
	}
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			out.Set(reflect.New(v.Type().Elem()))
			out.Elem().Set(copyValue(v.Elem(), norm))
		}
	case reflect.Slice:
		if !v.IsNil() {
			out.Set(reflect.MakeSlice(v.Type(), v.Len(), v.Len()))
			// Bytes hold nothing to normalise, and an upload session's state
			// can be megabytes (an S3 session keeps its pending part):
			// copying it a reflected element at a time took minutes under
			// -race.
			if v.Type().Elem().Kind() == reflect.Uint8 {
				reflect.Copy(out, v)
				break
			}
			for i := range v.Len() {
				out.Index(i).Set(copyValue(v.Index(i), norm))
			}
		}
	case reflect.Map:
		if !v.IsNil() {
			out.Set(reflect.MakeMapWithSize(v.Type(), v.Len()))
			for _, k := range v.MapKeys() {
				out.SetMapIndex(copyValue(k, norm), copyValue(v.MapIndex(k), norm))
			}
		}
	case reflect.Array:
		for i := range v.Len() {
			out.Index(i).Set(copyValue(v.Index(i), norm))
		}
	case reflect.Struct:
		out.Set(v)
		for i := range v.NumField() {
			if out.Field(i).CanSet() {
				out.Field(i).Set(copyValue(v.Field(i), norm))
			}
		}
	case reflect.Interface:
		if !v.IsNil() {
			out.Set(copyValue(v.Elem(), norm))
		}
	default:
		out.Set(v)
	}
	return out
}
