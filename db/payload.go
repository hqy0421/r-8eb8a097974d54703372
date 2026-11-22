package db

import (
	"encoding/binary"
)

// cellPayload represents the payload part of a cell. If overflow is non-zero the
// cellPayload field will be truncated. Use addOverflow() to get a full payload.
type cellPayload struct {
	Length   int64
	Payload  []byte
	Overflow int
}

// overflow is stored on different pages. Load whatever is needed to complete
// the payload data.
func addOverflow(db *Database, pl cellPayload) ([]byte, error) {
	to := pl.Payload
	overflow := pl.Overflow
	for {
		if overflow == 0 {
			if int64(len(to)) < pl.Length {
				return nil, ErrCorrupted
			}
			return to[:pl.Length], nil
		}
		buf, err := db.page(overflow)
		if err != nil {
			return nil, err
		}
		next, buf := int(binary.BigEndian.Uint32(buf[:4])), buf[4:]
		to = append(to, buf...)
		overflow = next
	}
}

// addOverflowLimit loads overflow pages only until at least limit bytes of the
// payload are available. This allows reading record header and short fields
// without walking the whole overflow chain of a large value (e.g. a BLOB that
// spans many pages). The returned slice may be longer than limit; it contains
// everything that got loaded, and is at most the full payload. limit is capped
// at the payload length.
func addOverflowLimit(db *Database, pl cellPayload, limit int64) ([]byte, error) {
	if limit < 0 || limit > pl.Length {
		return nil, ErrCorrupted
	}
	to := pl.Payload
	overflow := pl.Overflow
	for {
		if int64(len(to)) >= limit || overflow == 0 {
			if int64(len(to)) < limit {
				// chain ended before the requested amount was available
				return nil, ErrCorrupted
			}
			if int64(len(to)) > pl.Length {
				return to[:pl.Length], nil
			}
			return to, nil
		}
		buf, err := db.page(overflow)
		if err != nil {
			return nil, err
		}
		next, buf := int(binary.BigEndian.Uint32(buf[:4])), buf[4:]
		to = append(to, buf...)
		overflow = next
	}
}
