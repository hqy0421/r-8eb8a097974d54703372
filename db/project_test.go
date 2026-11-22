package db

import (
	"os"
	"reflect"
	"testing"
)

// countingPager wraps a database file and records which pages get read, so
// tests can assert that a projected read skips the tail of an overflow chain.
type countingPager struct {
	pages map[int]int
	// btree pages are read through Database.openPage; everything else is an
	// overflow page (or the header page).
	btree map[int]bool
}

func newCountingPager(path string, pagesize int) (*Database, *countingPager, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	cp := &countingPager{pages: map[int]int{}, btree: map[int]bool{}}
	d, err := newDatabase(&memPager{data: buf, pageSize: pagesize, cp: cp}, "")
	if err != nil {
		return nil, nil, err
	}
	d.pageHook = cp.markBtree
	return d, cp, nil
}

func (c *countingPager) record(id int) {
	c.pages[id]++
}

// markBtree records that id is a b-tree node page rather than an overflow page.
func (c *countingPager) markBtree(id int) {
	c.btree[id] = true
}

// Overflow returns the number of distinct overflow pages that were read.
func (c *countingPager) Overflow() int {
	n := 0
	for id := range c.pages {
		if id == 1 || c.btree[id] {
			continue
		}
		n++
	}
	return n
}

// OverflowFrom is kept for readability at call sites that think in terms of a
// b-tree root; the classification is already handled via markBtree.
func (c *countingPager) OverflowFrom(root int) int {
	return c.Overflow()
}

type memPager struct {
	data     []byte
	pageSize int
	cp       *countingPager
}

func (m *memPager) page(n int, pagesize int) ([]byte, error) {
	start := int64(n-1) * int64(m.pageSize)
	if start >= int64(len(m.data)) {
		return nil, ErrCorrupted
	}
	end := start + int64(m.pageSize)
	if end > int64(len(m.data)) {
		end = int64(len(m.data))
	}
	m.cp.record(n)
	buf := make([]byte, end-start)
	copy(buf, m.data[start:end])
	return buf, nil
}

func (m *memPager) Close() error                     { return nil }
func (m *memPager) RLock() error                     { return nil }
func (m *memPager) RUnlock() error                   { return nil }
func (m *memPager) CheckReservedLock() (bool, error) { return false, nil }

func TestAddOverflowLimit(t *testing.T) {
	db, err := OpenFile("./../testdata/sensor.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	table, err := db.Table("sensor")
	if err != nil {
		t.Fatal(err)
	}

	var pl cellPayload
	root, err := db.openTable(table.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Iter(maxRecursion, db, func(rowid int64, p cellPayload) (bool, error) {
		if rowid == 3 {
			pl = p
		}
		return rowid == 3, nil
	}); err != nil {
		t.Fatal(err)
	}
	if pl.Overflow == 0 {
		t.Fatal("test row has no overflow, the fixture is wrong")
	}

	full, err := addOverflow(db, pl)
	if err != nil {
		t.Fatal(err)
	}
	if have, want := len(full), int(pl.Length); have != want {
		t.Fatalf("full payload: have %d bytes, want %d", have, want)
	}

	// only the in-page part plus the first overflow page should be needed for
	// a small limit
	limited, err := addOverflowLimit(db, pl, int64(len(pl.Payload)+8))
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) < len(pl.Payload)+8 {
		t.Fatalf("limited read returned %d bytes", len(limited))
	}
	if len(limited) >= len(full) {
		t.Fatal("limited read unexpectedly loaded the whole payload")
	}

	// a limit at the full length must return everything
	again, err := addOverflowLimit(db, pl, pl.Length)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, full) {
		t.Fatal("limit at full length does not match addOverflow")
	}

	// a limit beyond the payload must fail as corruption
	if _, err := addOverflowLimit(db, pl, pl.Length+1); err != ErrCorrupted {
		t.Fatalf("have %v, want ErrCorrupted", err)
	}
}

func TestProjectedSkipsOverflowChain(t *testing.T) {
	db, cp, err := newCountingPager("./../testdata/sensor.sqlite", 512)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	table, err := db.Table("sensor")
	if err != nil {
		t.Fatal(err)
	}

	// full read: every overflow page of every row is visited
	fullRows := 0
	if err := table.Scan(func(rowid int64, rec Record) bool {
		fullRows++
		return false
	}); err != nil {
		t.Fatal(err)
	}
	fullOverflow := cp.Overflow()
	if fullOverflow == 0 {
		t.Fatal("fixture has no overflow pages")
	}
	if have, want := fullRows, 8; have != want {
		t.Fatalf("have %d rows, want %d", have, want)
	}

	// projected read of device_no (field 0) and length (field 1): both are
	// before the attachment, so no overflow page should be needed
	cp.pages = map[int]int{}
	var got [][2]interface{}
	if err := table.ScanColumns(map[int]bool{0: true, 1: true}, func(rowid int64, rec Record) bool {
		got = append(got, [2]interface{}{rec[0], rec[1]})
		// unrequested fields stay nil, including the big attachment
		if rec[2] != nil || rec[3] != nil || rec[4] != nil {
			t.Fatalf("unrequested fields were parsed: %#v", rec)
		}
		return false
	}); err != nil {
		t.Fatal(err)
	}
	if have, want := cp.Overflow(), 0; have != want {
		t.Fatalf("projected read touched %d overflow pages, want %d", have, want)
	}
	if have, want := len(got), fullRows; have != want {
		t.Fatalf("have %d projected rows, want %d", have, want)
	}
	if have, want := got[0], [2]interface{}{"dev-001", int64(12)}; !reflect.DeepEqual(have, want) {
		t.Errorf("first row: have %#v, want %#v", have, want)
	}
	if have, want := got[1], [2]interface{}{"dev-002", int64(1002)}; !reflect.DeepEqual(have, want) {
		t.Errorf("second row: have %#v, want %#v", have, want)
	}
}

func TestProjectedFieldsAfterOverflow(t *testing.T) {
	// fields stored after the overflowing attachment (note, reading) must
	// still parse correctly, which means following the overflow chain far
	// enough to reach their bytes.
	db, err := OpenFile("./../testdata/sensor.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	table, err := db.Table("sensor")
	if err != nil {
		t.Fatal(err)
	}

	// full record as the reference
	want := map[int64]Record{}
	if err := table.Scan(func(rowid int64, rec Record) bool {
		want[rowid] = rec
		return false
	}); err != nil {
		t.Fatal(err)
	}

	if err := table.ScanColumns(map[int]bool{3: true, 4: true}, func(rowid int64, rec Record) bool {
		w := want[rowid]
		if !reflect.DeepEqual(rec[3], w[3]) || !reflect.DeepEqual(rec[4], w[4]) {
			t.Errorf("row %d: have (%#v, %#v), want (%#v, %#v)",
				rowid, rec[3], rec[4], w[3], w[4])
		}
		if rec[0] != nil || rec[1] != nil || rec[2] != nil {
			t.Errorf("row %d: unrequested fields parsed: %#v", rowid, rec)
		}
		return false
	}); err != nil {
		t.Fatal(err)
	}

	// a single row lookup with the same projection
	rec, err := table.RowidColumns(4, map[int]bool{0: true, 3: true, 4: true})
	if err != nil {
		t.Fatal(err)
	}
	w := want[4]
	if !reflect.DeepEqual(rec[0], w[0]) ||
		!reflect.DeepEqual(rec[3], w[3]) ||
		!reflect.DeepEqual(rec[4], w[4]) {
		t.Errorf("RowidColumns: have %#v, want %#v", rec, w)
	}
}

func TestProjectedAttachmentIsComplete(t *testing.T) {
	// when the attachment itself is requested, the whole chain is read and
	// the value matches a full scan.
	db, err := OpenFile("./../testdata/sensor.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	table, err := db.Table("sensor")
	if err != nil {
		t.Fatal(err)
	}

	want := map[int64]interface{}{}
	if err := table.Scan(func(rowid int64, rec Record) bool {
		want[rowid] = rec[2]
		return false
	}); err != nil {
		t.Fatal(err)
	}

	if err := table.ScanColumns(map[int]bool{2: true}, func(rowid int64, rec Record) bool {
		if !reflect.DeepEqual(rec[2], want[rowid]) {
			t.Errorf("row %d: attachment mismatch", rowid)
		}
		return false
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProjectedNonRowidSkipsOverflow(t *testing.T) {
	db, cp, err := newCountingPager("./../testdata/sensor_norowid.sqlite", 512)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tab, err := db.NonRowidTable("sensor_nr")
	if err != nil {
		t.Fatal(err)
	}

	// full scan reference
	full := map[string]Record{}
	if err := tab.Scan(func(rec Record) bool {
		full[rec[0].(string)] = rec
		return false
	}); err != nil {
		t.Fatal(err)
	}
	if have, want := len(full), 7; have != want {
		t.Fatalf("have %d rows, want %d", have, want)
	}

	// projected scan of the key and the integer field: no overflow needed
	cp.pages = map[int]int{}
	seen := 0
	if err := tab.ScanColumns(map[int]bool{0: true, 1: true}, func(rec Record) bool {
		seen++
		w := full[rec[0].(string)]
		if !reflect.DeepEqual(rec[0], w[0]) || !reflect.DeepEqual(rec[1], w[1]) {
			t.Errorf("have %#v, want %#v", rec[:2], w[:2])
		}
		if rec[2] != nil || rec[3] != nil {
			t.Errorf("unrequested fields parsed: %#v", rec)
		}
		return false
	}); err != nil {
		t.Fatal(err)
	}
	if seen != len(full) {
		t.Fatalf("have %d projected rows, want %d", seen, len(full))
	}
	if have, want := cp.OverflowFrom(tab.root), 0; have != want {
		t.Fatalf("projected scan read %d overflow pages, want %d", have, want)
	}
}

func TestProjectedNonRowidFieldsAfterOverflow(t *testing.T) {
	db, err := OpenFile("./../testdata/sensor_norowid.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tab, err := db.NonRowidTable("sensor_nr")
	if err != nil {
		t.Fatal(err)
	}

	full := map[string]Record{}
	var order []string
	if err := tab.Scan(func(rec Record) bool {
		full[rec[0].(string)] = rec
		order = append(order, rec[0].(string))
		return false
	}); err != nil {
		t.Fatal(err)
	}

	// `note` is stored after the overflowing attachment
	idx := 0
	if err := tab.ScanColumns(map[int]bool{3: true}, func(rec Record) bool {
		w := full[order[idx]]
		idx++
		if rec[0] != nil {
			t.Errorf("key field was not requested but got %#v", rec[0])
		}
		if !reflect.DeepEqual(rec[3], w[3]) {
			t.Errorf("note: have %#v, want %#v", rec[3], w[3])
		}
		return false
	}); err != nil {
		t.Fatal(err)
	}
	if idx != len(order) {
		t.Fatalf("saw %d rows, want %d", idx, len(order))
	}

	// equality search with a projection: key columns must be parsed for the
	// match, requested columns come back correctly.
	key := Key{
		{V: "dev-004"},
	}
	var found Record
	if err := tab.ScanEqColumns(key, map[int]bool{1: true, 3: true}, func(rec Record) bool {
		found = rec
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatal("row not found")
	}
	w := full["dev-004"]
	if !reflect.DeepEqual(found[1], w[1]) || !reflect.DeepEqual(found[3], w[3]) {
		t.Errorf("have %#v, want fields %#v / %#v", found, w[1], w[3])
	}
}

func TestProjectedRowidLookupSkipsOverflow(t *testing.T) {
	// a single-row rowid lookup for short fields before the attachment must
	// not open any overflow page.
	db, cp, err := newCountingPager("./../testdata/sensor.sqlite", 512)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	table, err := db.Table("sensor")
	if err != nil {
		t.Fatal(err)
	}
	cp.pages = map[int]int{}
	rec, err := table.RowidColumns(6, map[int]bool{0: true, 1: true})
	if err != nil {
		t.Fatal(err)
	}
	if have, want := rec[0], "dev-006"; have != want {
		t.Errorf("have %v, want %v", have, want)
	}
	if have, want := rec[1], int64(1006); have != want {
		t.Errorf("have %v, want %v", have, want)
	}
	if have, want := cp.Overflow(), 0; have != want {
		t.Errorf("rowid lookup read %d overflow pages, want %d", have, want)
	}
}

func TestProjectedEmptyFieldSet(t *testing.T) {
	// an empty projection only needs the record header; the attachment's
	// overflow chain must not be touched at all.
	db, cp, err := newCountingPager("./../testdata/sensor.sqlite", 512)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	table, err := db.Table("sensor")
	if err != nil {
		t.Fatal(err)
	}
	cp.pages = map[int]int{}
	rec, err := table.RowidColumns(6, map[int]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if have, want := len(rec), 5; have != want {
		t.Errorf("record has %d fields, want %d", have, want)
	}
	for i, v := range rec {
		if v != nil {
			t.Errorf("field %d should be nil in an empty projection, got %#v", i, v)
		}
	}
	if have, want := cp.Overflow(), 0; have != want {
		t.Errorf("empty projection read %d overflow pages, want %d", have, want)
	}
}

func TestFullReadVisitsOverflowChain(t *testing.T) {
	// guard against the counting machinery silently classifying everything as
	// a b-tree page: a full scan must walk the overflow chains, and reading a
	// field stored after the attachment must reach deeper into the chain than
	// reading a field before it.
	db, cp, err := newCountingPager("./../testdata/sensor.sqlite", 512)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	table, err := db.Table("sensor")
	if err != nil {
		t.Fatal(err)
	}

	if err := table.Scan(func(int64, Record) bool { return false }); err != nil {
		t.Fatal(err)
	}
	fullReads := cp.Overflow()
	if fullReads == 0 {
		t.Fatal("full scan read no overflow pages; the counter cannot be trusted")
	}

	// short fields before the attachment need no overflow page at all
	cp.pages = map[int]int{}
	if err := table.ScanColumns(map[int]bool{0: true, 1: true}, func(int64, Record) bool {
		return false
	}); err != nil {
		t.Fatal(err)
	}
	beforeReads := cp.Overflow()

	// the note field is after the attachment, so its bytes are deep in the
	// overflow chain
	cp.pages = map[int]int{}
	if err := table.ScanColumns(map[int]bool{3: true}, func(int64, Record) bool {
		return false
	}); err != nil {
		t.Fatal(err)
	}
	afterReads := cp.Overflow()

	if beforeReads != 0 {
		t.Errorf("leading fields: have %d overflow reads, want 0", beforeReads)
	}
	if afterReads <= beforeReads {
		t.Errorf("trailing field read %d overflow pages, expected more than %d",
			afterReads, beforeReads)
	}
	// reaching a field after the attachment requires walking past the whole
	// attachment, which is every overflow page of every row — the same amount
	// as a full read.
	if afterReads != fullReads {
		t.Errorf("trailing field read %d overflow pages, want %d (full chain)",
			afterReads, fullReads)
	}
}
