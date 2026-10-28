package conda

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"github.com/tinylib/msgp/msgp"
)

// MessagePack container headers are attacker-controlled. Check depth, node
// count and remaining bytes before allocating, including in unknown fields.
func unpack(data []byte, depth int, budget *int) (any, []byte, error) {
	*budget--
	if depth > 32 || *budget < 0 || len(data) == 0 {
		return nil, nil, ErrInvalidRepodata
	}
	switch msgp.NextType(data) {
	case msgp.MapType:
		n, rest, err := msgp.ReadMapHeaderBytes(data)
		if err != nil || n > maxRepodataItems || uint64(n)*2 > uint64(len(rest)) ||
			uint64(n) > uint64(*budget) {
			return nil, nil, ErrInvalidRepodata
		}
		out := make(map[string]any, n)
		for range n {
			key, tail, err := msgp.ReadStringBytes(rest)
			if err != nil || !utf8.ValidString(key) {
				return nil, nil, ErrInvalidRepodata
			}
			if _, duplicate := out[key]; duplicate {
				return nil, nil, ErrInvalidRepodata
			}
			value, tail, err := unpack(tail, depth+1, budget)
			if err != nil {
				return nil, nil, err
			}
			out[key], rest = value, tail
		}
		return out, rest, nil
	case msgp.ArrayType:
		n, rest, err := msgp.ReadArrayHeaderBytes(data)
		if err != nil || n > maxRepodataItems || uint64(n) > uint64(len(rest)) ||
			uint64(n) > uint64(*budget) {
			return nil, nil, ErrInvalidRepodata
		}
		out := make([]any, n)
		for i := range out {
			out[i], rest, err = unpack(rest, depth+1, budget)
			if err != nil {
				return nil, nil, err
			}
		}
		return out, rest, nil
	case msgp.StrType:
		value, rest, err := msgp.ReadStringBytes(data)
		if !utf8.ValidString(value) {
			return nil, nil, ErrInvalidRepodata
		}
		return value, rest, err //nolint:wrapcheck // Private codec; caller adds protocol context.
	case msgp.BinType:
		return msgp.ReadBytesZC(data) //nolint:wrapcheck
	case msgp.IntType:
		return msgp.ReadInt64Bytes(data) //nolint:wrapcheck
	case msgp.UintType:
		return msgp.ReadUint64Bytes(data) //nolint:wrapcheck
	case msgp.BoolType:
		return msgp.ReadBoolBytes(data) //nolint:wrapcheck
	case msgp.Float32Type, msgp.Float64Type:
		return msgp.ReadFloat64Bytes(data) //nolint:wrapcheck
	case msgp.ExtensionType, msgp.TimeType, msgp.Complex64Type, msgp.Complex128Type:
		rest, err := msgp.Skip(data)
		return nil, rest, err //nolint:wrapcheck // Unknown extension fields are ignored after bounded framing.
	case msgp.NilType:
		rest, err := msgp.ReadNilBytes(data)
		return nil, rest, err //nolint:wrapcheck
	default:
		return nil, nil, ErrInvalidRepodata
	}
}

func decodeMessagePack(body []byte) (map[string]any, error) {
	if len(body) == 0 || len(body) > maxRepodataBytes {
		return nil, ErrInvalidRepodata
	}
	decoder, err := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(maxRepodataBytes), zstd.WithDecoderMaxWindow(maxRepodataBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: zstd reader", ErrInvalidRepodata)
	}
	defer decoder.Close()
	raw, err := io.ReadAll(io.LimitReader(decoder, maxRepodataBytes+1))
	if err != nil || len(raw) > maxRepodataBytes {
		return nil, ErrInvalidRepodata
	}
	budget := 1_000_000
	value, rest, err := unpack(raw, 0, &budget)
	out, ok := value.(map[string]any)
	if err != nil || len(rest) != 0 || !ok {
		return nil, ErrInvalidRepodata
	}
	return out, nil
}

func pack(dst []byte, value any) ([]byte, error) {
	switch v := value.(type) {
	case map[string]any:
		size := len(v)
		if size > maxRepodataItems {
			return nil, ErrInvalidRepodata
		}
		dst = msgp.AppendMapHeader(
			dst,
			uint32(size),
		)
		for _, k := range slices.Sorted(maps.Keys(v)) {
			var err error
			dst, err = pack(msgp.AppendString(dst, k), v[k])
			if err != nil {
				return nil, err
			}
		}
		return dst, nil
	case []any:
		size := len(v)
		if size > maxRepodataItems {
			return nil, ErrInvalidRepodata
		}
		dst = msgp.AppendArrayHeader(
			dst,
			uint32(size),
		)
		for _, item := range v {
			var err error
			dst, err = pack(dst, item)
			if err != nil {
				return nil, err
			}
		}
		return dst, nil
	default:
		out, err := msgp.AppendIntf(dst, value)
		return out, err //nolint:wrapcheck // Private codec; caller adds protocol context.
	}
}

func encodeMessagePack(value map[string]any) ([]byte, error) {
	raw, err := pack(nil, value)
	if err != nil || len(raw) > maxRepodataBytes {
		return nil, ErrInvalidRepodata
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, fmt.Errorf("conda: zstd encoder: %w", err)
	}
	defer func() { _ = encoder.Close() }()
	return encoder.EncodeAll(raw, nil), nil
}

func jsonMap(value any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("conda: encode record: %w", err)
	}
	var out map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&out); err != nil {
		return nil, fmt.Errorf("conda: decode record: %w", err)
	}
	return out, nil
}
