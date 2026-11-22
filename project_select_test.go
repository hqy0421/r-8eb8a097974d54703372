package sqlittle

import (
	"reflect"
	"testing"
)

// projected reads must return the same values as full reads while skipping the
// overflow chain of unrequested attachments.
func TestSelectProjectionOverflow(t *testing.T) {
	db, err := Open("testdata/sensor.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// reference: full select
	full := map[string][]string{}
	if err := db.Select("sensor", func(row Row) {
		full[row.ScanStrings()[0]] = row.ScanStrings()
	}, "device_no", "length", "attachment", "note", "reading"); err != nil {
		t.Fatal(err)
	}

	// projection on the short fields only; attachment is never requested
	var devices [][2]string
	if err := db.Select("sensor", func(row Row) {
		s := row.ScanStrings()
		devices = append(devices, [2]string{s[0], s[1]})
	}, "device_no", "length"); err != nil {
		t.Fatal(err)
	}
	if have, want := len(devices), len(full); have != want {
		t.Fatalf("have %d rows, want %d", have, want)
	}
	for _, d := range devices {
		w := full[d[0]]
		if d[0] != w[0] || d[1] != w[1] {
			t.Errorf("have %v, want %v", d, w[:2])
		}
	}

	// fields after the attachment must still parse correctly
	var tails [][3]string
	if err := db.Select("sensor", func(row Row) {
		s := row.ScanStrings()
		tails = append(tails, [3]string{s[0], s[1], s[2]})
	}, "device_no", "note", "reading"); err != nil {
		t.Fatal(err)
	}
	for _, got := range tails {
		w := full[got[0]]
		if got[1] != w[3] || got[2] != w[4] {
			t.Errorf("tail fields: have %v, want %v", got, [3]string{w[0], w[3], w[4]})
		}
	}
}

func TestSelectRowidProjectionOverflow(t *testing.T) {
	db, err := Open("testdata/sensor.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	row, err := db.SelectRowid("sensor", 4, "device_no", "length", "note", "reading")
	if err != nil {
		t.Fatal(err)
	}
	if row == nil {
		t.Fatal("row not found")
	}
	s := row.ScanStrings()
	want := []string{"dev-004", "1004", "note-4", "24.5"}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("have %v, want %v", s, want)
	}

	// rowid alias only: no record field is needed at all
	rowidRow, err := db.SelectRowid("sensor", 4, "rowid")
	if err != nil {
		t.Fatal(err)
	}
	var rid int64
	if err := rowidRow.Scan(&rid); err != nil {
		t.Fatal(err)
	}
	if rid != 4 {
		t.Errorf("have rowid %d, want 4", rid)
	}
}

func TestSelectAttachmentStillComplete(t *testing.T) {
	// requesting the attachment reads the complete overflow chain
	db, err := Open("testdata/sensor.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	full := map[string][]byte{}
	if err := db.Select("sensor", func(row Row) {
		var dev string
		var blob []byte
		if err := row.Scan(&dev, &blob); err != nil {
			t.Fatal(err)
		}
		full[dev] = blob
	}, "device_no", "attachment"); err != nil {
		t.Fatal(err)
	}

	// compare against sqlite's own view of the data
	wantLengths := map[string]int{
		"dev-002": 8274,
		"dev-003": 8411,
		"dev-004": 8548,
	}
	for dev, wantLen := range wantLengths {
		if have := len(full[dev]); have != wantLen {
			t.Errorf("%s: have %d bytes, want %d", dev, have, wantLen)
		}
	}
	if full["dev-001"] != nil {
		t.Errorf("NULL attachment should stay nil, got %d bytes", len(full["dev-001"]))
	}
}

func TestSelectProjectionNonRowid(t *testing.T) {
	db, err := Open("testdata/sensor_norowid.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// full reference
	full := map[string][]string{}
	if err := db.Select("sensor_nr", func(row Row) {
		s := row.ScanStrings()
		full[s[0]] = s
	}, "device_no", "length", "attachment", "note"); err != nil {
		t.Fatal(err)
	}

	// short fields before the attachment
	var seen [][2]string
	if err := db.Select("sensor_nr", func(row Row) {
		s := row.ScanStrings()
		seen = append(seen, [2]string{s[0], s[1]})
	}, "device_no", "length"); err != nil {
		t.Fatal(err)
	}
	for _, got := range seen {
		w := full[got[0]]
		if got[1] != w[1] {
			t.Errorf("have %v, want %v", got, w[:2])
		}
	}

	// the field after the overflowing attachment
	if err := db.Select("sensor_nr", func(row Row) {
		s := row.ScanStrings()
		w := full[s[0]]
		if s[1] != w[3] {
			t.Errorf("note: have %q, want %q", s[1], w[3])
		}
	}, "device_no", "note"); err != nil {
		t.Fatal(err)
	}

	// PKSelect equality path with a projection
	var got Row
	if err := db.PKSelect("sensor_nr", Key{"dev-005"}, func(row Row) {
		got = row
	}, "length", "note"); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("PKSelect found nothing")
	}
	s := got.ScanStrings()
	w := full["dev-005"]
	if s[0] != w[1] || s[1] != w[3] {
		t.Errorf("PKSelect: have %v, want %v", s, []string{w[1], w[3]})
	}
}

func TestIndexedSelectProjectionOverflow(t *testing.T) {
	// rowid table reached through a secondary index: the table lookup back
	// must still skip the unrequested attachment's overflow chain.
	db, err := Open("testdata/sensor.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	full := map[string][]string{}
	if err := db.Select("sensor", func(row Row) {
		full[row.ScanStrings()[0]] = row.ScanStrings()
	}, "device_no", "length", "attachment", "note", "reading"); err != nil {
		t.Fatal(err)
	}

	var seen [][2]string
	if err := db.IndexedSelect("sensor", "sensor_length", func(row Row) {
		s := row.ScanStrings()
		seen = append(seen, [2]string{s[0], s[1]})
	}, "device_no", "length"); err != nil {
		t.Fatal(err)
	}
	if have, want := len(seen), len(full); have != want {
		t.Fatalf("have %d rows, want %d", have, want)
	}
	for _, got := range seen {
		w := full[got[0]]
		if got[1] != w[1] {
			t.Errorf("have %v, want %v", got, w[:2])
		}
	}

	// equality search on the index, asking for a field after the attachment
	var note string
	count := 0
	if err := db.IndexedSelectEq(
		"sensor",
		"sensor_length",
		Key{int64(1004)},
		func(row Row) {
			count++
			if err := row.Scan(nil, &note); err != nil {
				t.Fatal(err)
			}
		},
		"length", "note",
	); err != nil {
		t.Fatal(err)
	}
	if count != 1 || note != "note-4" {
		t.Errorf("have count=%d note=%q, want 1 / note-4", count, note)
	}
}
