//go:build go1.18

package parquet

import (
	"bytes"
	"reflect"
	"unsafe"

	"github.com/segmentio/parquet-go/deprecated"
)

func makeArray[T any](s []T) array {
	return array{
		ptr: *(*unsafe.Pointer)(unsafe.Pointer(&s)),
		len: len(s),
	}
}

func makeSlice[T any](a array) []T {
	return slice[T](a.ptr, a.len)
}

func slice[T any](p unsafe.Pointer, n int) []T {
	return unsafe.Slice((*T)(p), n)
}

// nullIndexFunc is the type of functions generated by calling nullIndexFuncOf.
//
// The function takes an array of values that it assumes the type of (based on
// the reflect.Type that it was created from) and returns the index of the first
// null value, or the length of the array if no values were null.
//
// A value is null if it is zero for number types, or if it is nil for a slice
// type. Struct values are never null.
type nullIndexFunc func(array) int

func nullIndexByte(a array) int {
	i := bytes.IndexByte(makeSlice[byte](a), 0)
	if i < 0 {
		i = a.len
	}
	return i
}

func nullIndexSlice(a array) int {
	const size = unsafe.Sizeof(([]byte)(nil))
	for i := 0; i < a.len; i++ {
		p := *(*unsafe.Pointer)(a.index(i, size, 0))
		if p == nil {
			return i
		}
	}
	return a.len
}

func nullIndex[T comparable](a array) int {
	var zero T
	for i, v := range makeSlice[T](a) {
		if v == zero {
			return i
		}
	}
	return a.len
}

func nullIndexFuncOf(t reflect.Type) nullIndexFunc {
	switch t {
	case reflect.TypeOf(deprecated.Int96{}):
		return nullIndex[deprecated.Int96]
	}

	switch t.Kind() {
	case reflect.Bool:
		return nullIndexByte

	case reflect.Int, reflect.Uint:
		return nullIndex[int]

	case reflect.Int8, reflect.Uint8:
		return nullIndexByte

	case reflect.Int16, reflect.Uint16:
		return nullIndex[int16]

	case reflect.Int32, reflect.Uint32:
		return nullIndex[int32]

	case reflect.Int64, reflect.Uint64:
		return nullIndex[int64]

	case reflect.Float32:
		return nullIndex[float32]

	case reflect.Float64:
		return nullIndex[float64]

	case reflect.String:
		return nullIndex[string]

	case reflect.Slice:
		return nullIndexSlice

	case reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return nullIndexFuncOfArray(t)
		}

	case reflect.Pointer:
		return nullIndex[unsafe.Pointer]

	case reflect.Struct:
		return func(a array) int { return a.len }
	}

	panic("cannot convert Go values of type " + t.String() + " to parquet value")
}

func nullIndexFuncOfArray(t reflect.Type) nullIndexFunc {
	arrayLen := t.Len()
	return func(a array) int {
		for i := 0; i < a.len; i++ {
			p := a.index(i, uintptr(arrayLen), 0)
			b := slice[byte](p, arrayLen)
			if bytes.Count(b, []byte{0}) == len(b) {
				return i
			}
		}
		return a.len
	}
}

// nonNullIndexFunc is the type of functions generated by calling
// nonNullIndexFuncOf.
//
// The function takes an array of values that it assumes the type of (based on
// the reflect.Type that it was created from) and returns the index of the first
// non-null value, or the length of the array if all values were null.
//
// A value is null if it is zero for number types, or if it is nil for a slice
// type. Struct values are never null.
type nonNullIndexFunc func(array) int

func nonNullIndexSlice(a array) int {
	const size = unsafe.Sizeof(([]byte)(nil))
	for i := 0; i < a.len; i++ {
		p := *(*unsafe.Pointer)(a.index(i, size, 0))
		if p != nil {
			return i
		}
	}
	return a.len
}

func nonNullIndex[T comparable](a array) int {
	var zero T
	for i, v := range makeSlice[T](a) {
		if v != zero {
			return i
		}
	}
	return a.len
}

func nonNullIndexFuncOf(t reflect.Type) nonNullIndexFunc {
	switch t {
	case reflect.TypeOf(deprecated.Int96{}):
		return nonNullIndex[deprecated.Int96]
	}

	switch t.Kind() {
	case reflect.Bool:
		return nonNullIndex[bool]

	case reflect.Int, reflect.Uint:
		return nonNullIndex[int]

	case reflect.Int8, reflect.Uint8:
		return nonNullIndex[int8]

	case reflect.Int16, reflect.Uint16:
		return nonNullIndex[int16]

	case reflect.Int32, reflect.Uint32:
		return nonNullIndex[int32]

	case reflect.Int64, reflect.Uint64:
		return nonNullIndex[int64]

	case reflect.Float32:
		return nonNullIndex[float32]

	case reflect.Float64:
		return nonNullIndex[float64]

	case reflect.String:
		return nonNullIndex[string]

	case reflect.Slice:
		return nonNullIndexSlice

	case reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return nonNullIndexFuncOfArray(t)
		}

	case reflect.Pointer:
		return nonNullIndex[unsafe.Pointer]

	case reflect.Struct:
		return func(array) int { return 0 }
	}

	panic("cannot convert Go values of type " + t.String() + " to parquet value")
}

func nonNullIndexFuncOfArray(t reflect.Type) nonNullIndexFunc {
	arrayLen := t.Len()
	return func(a array) int {
		for i := 0; i < a.len; i++ {
			p := a.index(i, uintptr(arrayLen), 0)
			b := slice[byte](p, arrayLen)
			if bytes.Count(b, []byte{0}) != len(b) {
				return i
			}
		}
		return a.len
	}
}

type columnLevels struct {
	columnIndex     int16
	repetitionDepth byte
	repetitionLevel byte
	definitionLevel byte
}

// columnBufferWriter is a type used to writes rows to column buffers.
type columnBufferWriter struct {
	columns []ColumnBuffer // column buffers where row values are written
	values  []Value        // buffer used to temporarily hold values
	maxLen  int            // max number of values written to the temp buffer
}

// writeRowsFunc is the type of functions that apply rows to a set of column
// buffers.
//
// - w is the columnBufferWriter holding the column buffers where the rows are
//   written.
//
// - rows is the array of Go values to write to the column buffers.
//
// - size is the size of Go values in the rows array (in bytes).
//
// - offset is the byte offset of the value being written in each element of the
//   rows array.
//
// - levels is used to track the column index, repetition and definition levels
//   of values when writing optional or repeated columns.
//
type writeRowsFunc func(w *columnBufferWriter, rows array, size, offset uintptr, levels columnLevels) error

// writeRowsFuncOf generates a writeRowsFunc function for the given Go type and
// parquet schema. The column path indicates the column that the function is
// being generated for in the parquet schema.
func writeRowsFuncOf(t reflect.Type, schema *Schema, path columnPath) writeRowsFunc {
	switch t {
	case reflect.TypeOf(deprecated.Int96{}):
		return (*columnBufferWriter).writeRowsInt96
	}

	switch t.Kind() {
	case reflect.Bool:
		return (*columnBufferWriter).writeRowsBool

	case reflect.Int, reflect.Uint:
		return (*columnBufferWriter).writeRowsInt

	case reflect.Int8, reflect.Uint8:
		return (*columnBufferWriter).writeRowsInt8

	case reflect.Int16, reflect.Uint16:
		return (*columnBufferWriter).writeRowsInt16

	case reflect.Int32, reflect.Uint32:
		return (*columnBufferWriter).writeRowsInt32

	case reflect.Int64, reflect.Uint64:
		return (*columnBufferWriter).writeRowsInt64

	case reflect.Float32:
		return (*columnBufferWriter).writeRowsFloat32

	case reflect.Float64:
		return (*columnBufferWriter).writeRowsFloat64

	case reflect.String:
		return (*columnBufferWriter).writeRowsString

	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return (*columnBufferWriter).writeRowsString
		} else {
			return writeRowsFuncOfSlice(t, schema, path)
		}

	case reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return writeRowsFuncOfArray(t, schema, path)
		}

	case reflect.Pointer:
		return writeRowsFuncOfPointer(t, schema, path)

	case reflect.Struct:
		return writeRowsFuncOfStruct(t, schema, path)

	case reflect.Map:
		return writeRowsFuncOfMap(t, schema, path)
	}

	panic("cannot convert Go values of type " + t.String() + " to parquet value")
}

func writeRowsFuncOfArray(t reflect.Type, schema *Schema, path columnPath) writeRowsFunc {
	len := t.Len()
	if len == 16 {
		return (*columnBufferWriter).writeRowsUUID
	}
	return func(w *columnBufferWriter, rows array, size, offset uintptr, levels columnLevels) error {
		return w.writeRowsArray(rows, size, offset, levels, len)
	}
}

func writeRowsFuncOfOptional(t reflect.Type, schema *Schema, path columnPath, writeRows writeRowsFunc) writeRowsFunc {
	nullIndex, nonNullIndex := nullIndexFuncOf(t), nonNullIndexFuncOf(t)
	return func(w *columnBufferWriter, rows array, size, offset uintptr, levels columnLevels) error {
		if rows.len == 0 {
			return writeRows(w, rows, size, 0, levels)
		}

		nonNullLevels := levels
		nonNullLevels.definitionLevel++
		// In this function, we are dealing with optional values which are
		// neither pointers nor slices; for example, a []int32 field marked
		// "optional" in its parent struct.
		//
		// We need to find zero values, which should be represented as nulls
		// in the parquet column. In order to minimize the calls to writeRows
		// and maximize throughput, we use the nullIndex and nonNullIndex
		// functions, which are type-specific implementations of the algorithm.
		//
		// Sections of the input that are contiguous nulls or non-nulls can be
		// sent to a single call to writeRows to be written to the underlying
		// buffer since they share the same definition level.
		//
		// This optimization is defeated by inputs alternating null and non-null
		// sequences of single values, we do not expect this condition to be a
		// common case.
		for i := 0; i < rows.len; {
			a := array{}
			p := rows.index(i, size, 0)
			j := i + nonNullIndex(array{ptr: p, len: rows.len - i})

			if i < j {
				a.ptr = p
				a.len = j - i
				if err := writeRows(w, a, size, 0, levels); err != nil {
					return err
				}
			}

			if j < rows.len {
				p = rows.index(j, size, 0)
				i = j
				j = j + nullIndex(array{ptr: p, len: rows.len - j})
				a.ptr = p
				a.len = j - i
				if err := writeRows(w, a, size, 0, nonNullLevels); err != nil {
					return err
				}
			}

			i = j
		}

		return nil
	}
}

func writeRowsFuncOfPointer(t reflect.Type, schema *Schema, path columnPath) writeRowsFunc {
	elemType := t.Elem()
	elemSize := elemType.Size()
	writeRows := writeRowsFuncOf(elemType, schema, path)

	if len(path) == 0 {
		// This code path is taken when generating a writeRowsFunc for a pointer
		// type. In this case, we do not need to increase the definition level
		// since we are not deailng with an optional field but a pointer to the
		// row type.
		return func(w *columnBufferWriter, rows array, size, _ uintptr, levels columnLevels) error {
			if rows.len == 0 {
				return writeRows(w, rows, size, 0, levels)
			}

			for i := 0; i < rows.len; i++ {
				p := *(*unsafe.Pointer)(rows.index(i, size, 0))
				a := array{}
				if p != nil {
					a.ptr = p
					a.len = 1
				}
				if err := writeRows(w, a, elemSize, 0, levels); err != nil {
					return err
				}
			}

			return nil
		}
	}

	return func(w *columnBufferWriter, rows array, size, offset uintptr, levels columnLevels) error {
		if rows.len == 0 {
			return writeRows(w, rows, size, 0, levels)
		}

		for i := 0; i < rows.len; i++ {
			p := *(*unsafe.Pointer)(rows.index(i, size, offset))
			a := array{}
			elemLevels := levels
			if p != nil {
				a.ptr = p
				a.len = 1
				elemLevels.definitionLevel++
			}
			if err := writeRows(w, a, elemSize, 0, elemLevels); err != nil {
				return err
			}
		}

		return nil
	}
}

func writeRowsFuncOfSlice(t reflect.Type, schema *Schema, path columnPath) writeRowsFunc {
	elemType := t.Elem()
	elemSize := elemType.Size()
	writeRows := writeRowsFuncOf(elemType, schema, path)
	return func(w *columnBufferWriter, rows array, size, offset uintptr, levels columnLevels) error {
		if rows.len == 0 {
			return writeRows(w, rows, size, 0, levels)
		}

		levels.repetitionDepth++

		for i := 0; i < rows.len; i++ {
			p := rows.index(i, size, offset)
			a := *(*array)(p)
			n := a.len

			elemLevels := levels
			if n > 0 {
				a.len = 1
				elemLevels.definitionLevel++
			}

			if err := writeRows(w, a, elemSize, 0, elemLevels); err != nil {
				return err
			}

			if n > 1 {
				elemLevels.repetitionLevel = elemLevels.repetitionDepth
				a.ptr = a.index(1, elemSize, 0)
				a.len = n - 1

				if err := writeRows(w, a, elemSize, 0, elemLevels); err != nil {
					return err
				}
			}
		}

		return nil
	}
}

func writeRowsFuncOfStruct(t reflect.Type, schema *Schema, path columnPath) writeRowsFunc {
	type column struct {
		columnIndex int16
		optional    bool
		offset      uintptr
		writeRows   writeRowsFunc
	}

	fields := structFieldsOf(t)
	columns := make([]column, len(fields))

	for i, f := range fields {
		optional := false
		columnPath := path.append(f.Name)
		forEachStructTagOption(f.Tag, func(option, _ string) {
			switch option {
			case "list":
				columnPath = columnPath.append("list", "element")
			case "optional":
				optional = true
			}
		})

		writeRows := writeRowsFuncOf(f.Type, schema, columnPath)
		if optional {
			switch f.Type.Kind() {
			case reflect.Pointer, reflect.Slice:
			default:
				writeRows = writeRowsFuncOfOptional(f.Type, schema, columnPath, writeRows)
			}
		}

		columnInfo := schema.mapping.lookup(columnPath)
		columns[i] = column{
			columnIndex: columnInfo.columnIndex,
			offset:      f.Offset,
			writeRows:   writeRows,
		}
	}

	return func(w *columnBufferWriter, rows array, size, offset uintptr, levels columnLevels) error {
		for _, column := range columns {
			levels.columnIndex = column.columnIndex
			if err := column.writeRows(w, rows, size, offset+column.offset, levels); err != nil {
				return err
			}
		}
		return nil
	}
}

func writeRowsFuncOfMap(t reflect.Type, schema *Schema, path columnPath) writeRowsFunc {
	keyPath := path.append("key_value", "key")
	keyType := t.Key()
	keySize := keyType.Size()
	writeKeys := writeRowsFuncOf(keyType, schema, keyPath)
	keyColumnIndex := schema.mapping.lookup(keyPath).columnIndex

	valuePath := path.append("key_value", "value")
	valueType := t.Elem()
	valueSize := valueType.Size()
	writeValues := writeRowsFuncOf(valueType, schema, valuePath)
	valueColumnIndex := schema.mapping.lookup(valuePath).columnIndex

	writeKeyValues := func(w *columnBufferWriter, keys, values array, levels columnLevels) error {
		levels.columnIndex = keyColumnIndex
		if err := writeKeys(w, keys, keySize, 0, levels); err != nil {
			return err
		}
		levels.columnIndex = valueColumnIndex
		if err := writeValues(w, values, valueSize, 0, levels); err != nil {
			return err
		}
		return nil
	}

	return func(w *columnBufferWriter, rows array, size, offset uintptr, levels columnLevels) error {
		if rows.len == 0 {
			return writeKeyValues(w, rows, rows, levels)
		}

		levels.repetitionDepth++
		mapKey := reflect.New(keyType).Elem()
		mapValue := reflect.New(valueType).Elem()

		for i := 0; i < rows.len; i++ {
			m := reflect.NewAt(t, rows.index(i, size, offset)).Elem()

			if m.Len() == 0 {
				if err := writeKeyValues(w, array{}, array{}, levels); err != nil {
					return err
				}
			} else {
				elemLevels := levels
				elemLevels.definitionLevel++

				for it := m.MapRange(); it.Next(); {
					mapKey.SetIterKey(it)
					mapValue.SetIterValue(it)

					k := array{ptr: addressOf(mapKey), len: 1}
					v := array{ptr: addressOf(mapValue), len: 1}

					if err := writeKeyValues(w, k, v, elemLevels); err != nil {
						return err
					}

					elemLevels.repetitionLevel = elemLevels.repetitionDepth
				}
			}
		}

		return nil
	}
}

func addressOf(v reflect.Value) unsafe.Pointer {
	return (*[2]unsafe.Pointer)(unsafe.Pointer(&v))[1]
}

func (w *columnBufferWriter) writeRowsBool(rows array, size, offset uintptr, levels columnLevels) (err error) {
	if rows.len == 0 {
		return w.writeRowsNull(levels)
	}

	switch c := w.columns[levels.columnIndex].(type) {
	case *booleanColumnBuffer:
		c.writeValues(rows, size, offset)
	default:
		w.reset()
		for i := 0; i < rows.len; i++ {
			p := rows.index(i, size, offset)
			v := makeValueBoolean(*(*bool)(p))
			v.repetitionLevel = levels.repetitionLevel
			v.definitionLevel = levels.definitionLevel
			w.values = append(w.values, v)
		}
		_, err = w.columns[levels.columnIndex].WriteValues(w.values)
	}

	return err
}

func (w *columnBufferWriter) writeRowsInt(rows array, size, offset uintptr, levels columnLevels) (err error) {
	if rows.len == 0 {
		return w.writeRowsNull(levels)
	}

	switch c := w.columns[levels.columnIndex].(type) {
	case *int64ColumnBuffer:
		c.writeValues(rows, size, offset)
	case *uint64ColumnBuffer:
		c.writeValues(rows, size, offset)
	default:
		w.reset()
		for i := 0; i < rows.len; i++ {
			p := rows.index(i, size, offset)
			v := makeValueInt64(int64(*(*int)(p)))
			v.repetitionLevel = levels.repetitionLevel
			v.definitionLevel = levels.definitionLevel
			w.values = append(w.values, v)
		}
		_, err = w.columns[levels.columnIndex].WriteValues(w.values)
	}

	return err
}

func (w *columnBufferWriter) writeRowsInt8(rows array, size, offset uintptr, levels columnLevels) (err error) {
	if rows.len == 0 {
		return w.writeRowsNull(levels)
	}

	switch c := w.columns[levels.columnIndex].(type) {
	case *int32ColumnBuffer:
		c.writeValues(rows, size, offset)
	case *uint32ColumnBuffer:
		c.writeValues(rows, size, offset)
	default:
		w.reset()
		for i := 0; i < rows.len; i++ {
			p := rows.index(i, size, offset)
			v := makeValueInt32(int32(*(*int8)(p)))
			v.repetitionLevel = levels.repetitionLevel
			v.definitionLevel = levels.definitionLevel
			w.values = append(w.values, v)
		}
		_, err = w.columns[levels.columnIndex].WriteValues(w.values)
	}

	return err
}

func (w *columnBufferWriter) writeRowsInt16(rows array, size, offset uintptr, levels columnLevels) (err error) {
	if rows.len == 0 {
		return w.writeRowsNull(levels)
	}

	switch c := w.columns[levels.columnIndex].(type) {
	case *int32ColumnBuffer:
		c.writeValues(rows, size, offset)
	case *uint32ColumnBuffer:
		c.writeValues(rows, size, offset)
	default:
		w.reset()
		for i := 0; i < rows.len; i++ {
			p := rows.index(i, size, offset)
			v := makeValueInt32(int32(*(*int16)(p)))
			v.repetitionLevel = levels.repetitionLevel
			v.definitionLevel = levels.definitionLevel
			w.values = append(w.values, v)
		}
		_, err = w.columns[levels.columnIndex].WriteValues(w.values)
	}

	return err
}

func (w *columnBufferWriter) writeRowsInt32(rows array, size, offset uintptr, levels columnLevels) (err error) {
	if rows.len == 0 {
		return w.writeRowsNull(levels)
	}

	switch c := w.columns[levels.columnIndex].(type) {
	case *int32ColumnBuffer:
		c.writeValues(rows, size, offset)
	case *uint32ColumnBuffer:
		c.writeValues(rows, size, offset)
	default:
		w.reset()
		for i := 0; i < rows.len; i++ {
			p := rows.index(i, size, offset)
			v := makeValueInt32(*(*int32)(p))
			v.repetitionLevel = levels.repetitionLevel
			v.definitionLevel = levels.definitionLevel
			w.values = append(w.values, v)
		}
		_, err = w.columns[levels.columnIndex].WriteValues(w.values)
	}

	return err
}

func (w *columnBufferWriter) writeRowsInt64(rows array, size, offset uintptr, levels columnLevels) (err error) {
	if rows.len == 0 {
		return w.writeRowsNull(levels)
	}

	switch c := w.columns[levels.columnIndex].(type) {
	case *int64ColumnBuffer:
		c.writeValues(rows, size, offset)
	case *uint64ColumnBuffer:
		c.writeValues(rows, size, offset)
	default:
		w.reset()
		for i := 0; i < rows.len; i++ {
			p := rows.index(i, size, offset)
			v := makeValueInt64(*(*int64)(p))
			v.repetitionLevel = levels.repetitionLevel
			v.definitionLevel = levels.definitionLevel
			w.values = append(w.values, v)
		}
		_, err = w.columns[levels.columnIndex].WriteValues(w.values)
	}

	return err
}

func (w *columnBufferWriter) writeRowsInt96(rows array, size, offset uintptr, levels columnLevels) (err error) {
	if rows.len == 0 {
		return w.writeRowsNull(levels)
	}

	switch c := w.columns[levels.columnIndex].(type) {
	case *int96ColumnBuffer:
		c.writeValues(rows, size, offset)
	default:
		w.reset()
		for i := 0; i < rows.len; i++ {
			p := rows.index(i, size, offset)
			v := makeValueInt96(*(*deprecated.Int96)(p))
			v.repetitionLevel = levels.repetitionLevel
			v.definitionLevel = levels.definitionLevel
			w.values = append(w.values, v)
		}
		_, err = w.columns[levels.columnIndex].WriteValues(w.values)
	}

	return err
}

func (w *columnBufferWriter) writeRowsFloat32(rows array, size, offset uintptr, levels columnLevels) (err error) {
	if rows.len == 0 {
		return w.writeRowsNull(levels)
	}

	switch c := w.columns[levels.columnIndex].(type) {
	case *floatColumnBuffer:
		c.writeValues(rows, size, offset)
	default:
		w.reset()
		for i := 0; i < rows.len; i++ {
			p := rows.index(i, size, offset)
			v := makeValueFloat(*(*float32)(p))
			v.repetitionLevel = levels.repetitionLevel
			v.definitionLevel = levels.definitionLevel
			w.values = append(w.values, v)
		}
		_, err = w.columns[levels.columnIndex].WriteValues(w.values)
	}

	return err
}

func (w *columnBufferWriter) writeRowsFloat64(rows array, size, offset uintptr, levels columnLevels) (err error) {
	if rows.len == 0 {
		return w.writeRowsNull(levels)
	}

	switch c := w.columns[levels.columnIndex].(type) {
	case *doubleColumnBuffer:
		c.writeValues(rows, size, offset)
	default:
		w.reset()
		for i := 0; i < rows.len; i++ {
			p := rows.index(i, size, offset)
			v := makeValueDouble(*(*float64)(p))
			v.repetitionLevel = levels.repetitionLevel
			v.definitionLevel = levels.definitionLevel
			w.values = append(w.values, v)
		}
		_, err = w.columns[levels.columnIndex].WriteValues(w.values)
	}

	return err
}

func (w *columnBufferWriter) writeRowsString(rows array, size, offset uintptr, levels columnLevels) (err error) {
	if rows.len == 0 {
		return w.writeRowsNull(levels)
	}

	switch c := w.columns[levels.columnIndex].(type) {
	case *byteArrayColumnBuffer:
		c.writeValues(rows, size, offset)
	default:
		w.reset()
		for i := 0; i < rows.len; i++ {
			p := rows.index(i, size, offset)
			v := makeValueString(ByteArray, *(*string)(p))
			v.repetitionLevel = levels.repetitionLevel
			v.definitionLevel = levels.definitionLevel
			w.values = append(w.values, v)
		}
		_, err = w.columns[levels.columnIndex].WriteValues(w.values)
	}

	return err
}

func (w *columnBufferWriter) writeRowsUUID(rows array, size, offset uintptr, levels columnLevels) (err error) {
	if rows.len == 0 {
		return w.writeRowsNull(levels)
	}

	switch c := w.columns[levels.columnIndex].(type) {
	case *fixedLenByteArrayColumnBuffer:
		c.writeValues128(rows, size, offset)
	default:
		w.reset()
		for i := 0; i < rows.len; i++ {
			p := rows.index(i, size, offset)
			v := makeValueByteArray(FixedLenByteArray, (*byte)(p), 16)
			v.repetitionLevel = levels.repetitionLevel
			v.definitionLevel = levels.definitionLevel
			w.values = append(w.values, v)
		}
		_, err = w.columns[levels.columnIndex].WriteValues(w.values)
	}

	return err
}

func (w *columnBufferWriter) writeRowsArray(rows array, size, offset uintptr, levels columnLevels, len int) (err error) {
	if rows.len == 0 {
		return w.writeRowsNull(levels)
	}

	switch c := w.columns[levels.columnIndex].(type) {
	case *fixedLenByteArrayColumnBuffer:
		c.writeValues(rows, size, offset)
	default:
		w.reset()
		for i := 0; i < rows.len; i++ {
			p := rows.index(i, size, offset)
			v := makeValueByteArray(FixedLenByteArray, (*byte)(p), len)
			v.repetitionLevel = levels.repetitionLevel
			v.definitionLevel = levels.definitionLevel
			w.values = append(w.values, v)
		}
		_, err = w.columns[levels.columnIndex].WriteValues(w.values)
	}

	return err
}

func (w *columnBufferWriter) writeRowsNull(levels columnLevels) error {
	w.reset()
	w.values = append(w.values[:0], Value{
		repetitionLevel: levels.repetitionLevel,
		definitionLevel: levels.definitionLevel,
	})
	_, err := w.columns[levels.columnIndex].WriteValues(w.values)
	return err
}

func (w *columnBufferWriter) reset() {
	if len(w.values) > w.maxLen {
		w.maxLen = len(w.values)
	}
	w.values = w.values[:0]
}

func (w *columnBufferWriter) clear() {
	clearValues(w.values[:w.maxLen])
	w.maxLen = 0
}
