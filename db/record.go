package db

import (
	"encoding/binary"
	"errors"
	"math"
)

var (
	errInternal = errors.New("unexpected record type found")
)

// Record is a row in the database.
// It can only have fields of these types: nil, int64, float64, string, []byte
type Record []interface{}

func parseRecord(r []byte) (Record, error) {
	h, err := parseRecordHeader(r)
	if err != nil {
		return nil, err
	}
	body := r[h.bodyStart:]
	var res Record
	for _, c := range h.serials {
		v, n, err := parseField(c, body)
		if err != nil {
			return res, err
		}
		res = append(res, v)
		body = body[n:]
	}
	return res, nil
}

// recordHeader is the parsed header of a record: the header size (including the
// header length varint), the serial type of every field, and the byte offset of
// the body within the record.
type recordHeader struct {
	headerSize int64
	bodyStart  int64
	serials    []int64
}

// parseRecordHeader reads the record header. It only needs the header bytes,
// which are always at the start of the record and hence always in the in-page
// part of a cell (the first overflow page is never touched for this).
func parseRecordHeader(r []byte) (recordHeader, error) {
	h := recordHeader{}
	hSize, n := readVarint(r)
	if n < 0 || hSize < int64(n) || hSize > int64(len(r)) {
		return h, ErrCorrupted
	}
	h.headerSize = hSize
	h.bodyStart = hSize
	header := r[n:hSize]
	for len(header) > 0 {
		c, n := readVarint(header)
		if n < 0 {
			return h, ErrCorrupted
		}
		header = header[n:]
		h.serials = append(h.serials, c)
	}
	return h, nil
}

// recordPayloadLimit reports how many payload bytes are needed to parse the
// fields named in fields: everything up to and including the last requested
// field. Fields not in the set are skipped, so a large unrequested value that
// lives on overflow pages never has to be read.
func recordPayloadLimit(h recordHeader, fields map[int]bool) int64 {
	var maxField int = -1
	for i := range h.serials {
		if fields[i] {
			maxField = i
		}
	}
	if maxField < 0 {
		// nothing requested beyond the header
		return h.headerSize
	}
	var off int64
	for i := 0; i <= maxField; i++ {
		off += serialLen(h.serials[i])
	}
	return h.bodyStart + off
}

// parseRecordFields parses the requested fields from a (possibly truncated)
// record. The returned Record has one entry per field in the header; entries for
// unrequested fields are nil. r must contain at least the bytes reported by
// recordPayloadLimit(). A requested field whose data runs past the end of r is
// reported as database corruption.
func parseRecordFields(r []byte, h recordHeader, fields map[int]bool) (Record, error) {
	res := make(Record, len(h.serials))
	maxField := -1
	for i := range h.serials {
		if fields[i] {
			maxField = i
		}
	}
	if maxField < 0 {
		// no requested fields; the header was all that was needed
		return res, nil
	}
	body := r[h.bodyStart:]
	// only walk up to the last requested field; fields after it may live on
	// overflow pages we deliberately did not load.
	for i := 0; i <= maxField; i++ {
		c := h.serials[i]
		l := serialLen(c)
		if int64(len(body)) < l {
			return res, ErrCorrupted
		}
		if !fields[i] {
			// skip the bytes without decoding the value
			body = body[l:]
			continue
		}
		v, n, err := parseField(c, body)
		if err != nil {
			return res, err
		}
		res[i] = v
		body = body[n:]
	}
	return res, nil
}

// serialLen gives the body size of a value with the given serial type.
func serialLen(c int64) int64 {
	switch {
	case c == 0 || c == 8 || c == 9:
		return 0
	case c == 10 || c == 11:
		// reserved internal types, they carry no value
		return 0
	case c >= 12 && c&1 == 0:
		// blob
		return (c - 12) / 2
	case c >= 13:
		// string
		return (c - 13) / 2
	default:
		// integer and float serial types 1..7
		return c
	}
}

// parseField parses a single value. It returns the value and the number of body
// bytes consumed.
func parseField(c int64, body []byte) (interface{}, int64, error) {
	switch c {
	case 0:
		// NULL
		return nil, 0, nil
	case 1:
		// 8-bit twos-complement integer.
		if len(body) < 1 {
			return nil, 0, ErrCorrupted
		}
		return int64(int8(body[0])), 1, nil
	case 2:
		// Value is a big-endian 16-bit twos-complement integer.
		if len(body) < 2 {
			return nil, 0, ErrCorrupted
		}
		return int64(int16(binary.BigEndian.Uint16(body[:2]))), 2, nil
	case 3:
		// Value is a big-endian 24-bit twos-complement integer.
		if len(body) < 3 {
			return nil, 0, ErrCorrupted
		}
		return readTwos24(body), 3, nil
	case 4:
		// Value is a big-endian 32-bit twos-complement integer.
		if len(body) < 4 {
			return nil, 0, ErrCorrupted
		}
		return int64(int32(binary.BigEndian.Uint32(body[:4]))), 4, nil
	case 5:
		// Value is a big-endian 48-bit twos-complement integer.
		if len(body) < 6 {
			return nil, 0, ErrCorrupted
		}
		return readTwos48(body), 6, nil
	case 6:
		// Value is a big-endian 64-bit twos-complement integer.
		if len(body) < 8 {
			return nil, 0, ErrCorrupted
		}
		return int64(binary.BigEndian.Uint64(body[:8])), 8, nil
	case 7:
		// Value is a big-endian IEEE 754-2008 64-bit floating point number.
		if len(body) < 8 {
			return nil, 0, ErrCorrupted
		}
		return math.Float64frombits(binary.BigEndian.Uint64(body[:8])), 8, nil
	case 8:
		// Value is the integer 0. (Only available for schema format 4 and higher.)
		return int64(0), 0, nil
	case 9:
		// Value is the integer 1. (Only available for schema format 4 and higher.)
		return int64(1), 0, nil
	case 10, 11:
		// internal types. Should not happen.
		return nil, 0, errInternal
	default:
		l := serialLen(c)
		if int64(len(body)) < l {
			return nil, 0, ErrCorrupted
		}
		p := body[:l]
		if c&1 == 0 {
			// even, blob
			return p, l, nil
		}
		// odd, string
		// TODO: deal with encoding
		return string(p), l, nil
	}
}

// Removes the rowid column from an index value (that's the last value from a
// Record).
// Returns: rowid, record, error
func ChompRowid(rec Record) (int64, Record, error) {
	if len(rec) == 0 {
		return 0, nil, errors.New("no fields in index")
	}
	rowid, ok := rec[len(rec)-1].(int64)
	if !ok {
		return 0, nil, errors.New("invalid rowid pointer in index")
	}
	rec = rec[:len(rec)-1]
	return rowid, rec, nil
}
