package cache

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// RowData is the row data replayed by the cache (raw cell values).
// RowData 为缓存重放的行数据（原始单元格值）。
type RowData struct {
	Columns []string
	Rows    [][]any
}

// Codec is the cache serialization interface; binaryCodec is the default.
// Codec 为缓存序列化接口，默认 binaryCodec。
type Codec interface {
	Encode(*RowData) ([]byte, error)
	Decode([]byte) (*RowData, error)
}

const (
	codecMagic0 = 0x52 // 'R'
	codecMagic1 = 0x53 // 'S'
	codecVer    = 1

	cellNull  = 0x00
	cellInt64 = 0x01
	cellFloat = 0x02
	cellBool  = 0x03
	cellStr   = 0x04
	cellBytes = 0x05
	cellTime  = 0x06
)

type binaryCodec struct{}

func (binaryCodec) Encode(rd *RowData) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte(codecMagic0)
	buf.WriteByte(codecMagic1)
	buf.WriteByte(codecVer)
	writeUvarint(&buf, uint64(len(rd.Columns)))
	for _, c := range rd.Columns {
		writeUvarint(&buf, uint64(len(c)))
		buf.WriteString(c)
	}
	writeUvarint(&buf, uint64(len(rd.Rows)))
	for _, row := range rd.Rows {
		if len(row) != len(rd.Columns) {
			return nil, fmt.Errorf("cache codec: row has %d cells, want %d", len(row), len(rd.Columns))
		}
		for _, v := range row {
			if err := writeCell(&buf, v); err != nil {
				return nil, err
			}
		}
	}
	return buf.Bytes(), nil
}

func (binaryCodec) Decode(data []byte) (*RowData, error) {
	if len(data) < 3 || data[0] != codecMagic0 || data[1] != codecMagic1 || data[2] != codecVer {
		return nil, fmt.Errorf("cache codec: invalid header")
	}
	r := &reader{buf: data[3:]}
	nCols, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	rd := &RowData{Columns: make([]string, 0, nCols)}
	for i := uint64(0); i < nCols; i++ {
		l, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		s, err := r.bytes(int(l))
		if err != nil {
			return nil, err
		}
		rd.Columns = append(rd.Columns, string(s))
	}
	nRows, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	rd.Rows = make([][]any, 0, nRows)
	for i := uint64(0); i < nRows; i++ {
		row := make([]any, len(rd.Columns))
		for j := range row {
			v, err := readCell(r)
			if err != nil {
				return nil, err
			}
			row[j] = v
		}
		rd.Rows = append(rd.Rows, row)
	}
	if r.remaining() != 0 {
		return nil, fmt.Errorf("cache codec: %d trailing bytes", r.remaining())
	}
	return rd, nil
}

func writeCell(buf *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case nil:
		buf.WriteByte(cellNull)
	case int64:
		buf.WriteByte(cellInt64)
		writeVarint(buf, v)
	case float64:
		buf.WriteByte(cellFloat)
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
		buf.Write(b[:])
	case bool:
		buf.WriteByte(cellBool)
		if v {
			buf.WriteByte(1)
		} else {
			buf.WriteByte(0)
		}
	case string:
		buf.WriteByte(cellStr)
		writeUvarint(buf, uint64(len(v)))
		buf.WriteString(v)
	case []byte:
		buf.WriteByte(cellBytes)
		writeUvarint(buf, uint64(len(v)))
		buf.Write(v)
	case time.Time:
		buf.WriteByte(cellTime)
		writeVarint(buf, v.UnixNano())
		_, offset := v.Zone()
		writeVarint(buf, int64(offset/60))
	default:
		return fmt.Errorf("cache codec: unsupported cell type %T", v)
	}
	return nil
}

func readCell(r *reader) (any, error) {
	tag, err := r.byte()
	if err != nil {
		return nil, err
	}
	switch tag {
	case cellNull:
		return nil, nil
	case cellInt64:
		n, err := r.varint()
		if err != nil {
			return nil, err
		}
		return n, nil
	case cellFloat:
		b, err := r.raw(8)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(binary.LittleEndian.Uint64(b)), nil
	case cellBool:
		b, err := r.byte()
		if err != nil {
			return nil, err
		}
		return b != 0, nil
	case cellStr:
		l, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		b, err := r.bytes(int(l))
		if err != nil {
			return nil, err
		}
		return string(b), nil
	case cellBytes:
		l, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		return r.bytes(int(l))
	case cellTime:
		nano, err := r.varint()
		if err != nil {
			return nil, err
		}
		offMin, err := r.varint()
		if err != nil {
			return nil, err
		}
		loc := time.FixedZone("", int(offMin)*60)
		return time.Unix(0, nano).In(loc), nil
	default:
		return nil, fmt.Errorf("cache codec: unknown cell tag 0x%02x", tag)
	}
}

func writeUvarint(buf *bytes.Buffer, v uint64) {
	var b [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(b[:], v)
	buf.Write(b[:n])
}

func writeVarint(buf *bytes.Buffer, v int64) {
	var b [binary.MaxVarintLen64]byte
	n := binary.PutVarint(b[:], v)
	buf.Write(b[:n])
}

type reader struct {
	buf []byte
	pos int
}

func (r *reader) remaining() int { return len(r.buf) - r.pos }
func (r *reader) byte() (byte, error) {
	if r.pos >= len(r.buf) {
		return 0, fmt.Errorf("cache codec: unexpected EOF")
	}
	b := r.buf[r.pos]
	r.pos++
	return b, nil
}
func (r *reader) raw(n int) ([]byte, error) {
	if n < 0 || r.pos+n > len(r.buf) {
		return nil, fmt.Errorf("cache codec: unexpected EOF")
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b, nil
}
func (r *reader) bytes(n int) ([]byte, error) { return r.raw(n) }
func (r *reader) uvarint() (uint64, error) {
	v, n := binary.Uvarint(r.buf[r.pos:])
	if n <= 0 {
		return 0, fmt.Errorf("cache codec: bad uvarint")
	}
	r.pos += n
	return v, nil
}
func (r *reader) varint() (int64, error) {
	v, n := binary.Varint(r.buf[r.pos:])
	if n <= 0 {
		return 0, fmt.Errorf("cache codec: bad varint")
	}
	r.pos += n
	return v, nil
}
