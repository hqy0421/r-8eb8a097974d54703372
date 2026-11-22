#!/bin/bash
set -eu

# A WITHOUT ROWID table with a large attachment spanning overflow pages, and
# short fields both before and after it. Used to test projected reads on the
# index b-tree code path.
rm -f sensor_norowid.sqlite
sqlite3 --batch sensor_norowid.sqlite <<'HERE'
PRAGMA page_size=512;
CREATE TABLE sensor_nr (
    device_no  TEXT PRIMARY KEY,
    length     INTEGER,
    attachment BLOB,
    note       TEXT
) WITHOUT ROWID;
HERE

python3 -I - <<'PY'
import sqlite3

rows = [("dev-001", 12, None, "no attachment")]
for i in range(2, 8):
    blob = bytes((j * 5 + i) & 0xff for j in range(7000 + i * 211))
    rows.append((f"dev-{i:03d}", 1000 + i, blob, f"note-{i}"))

con = sqlite3.connect("sensor_norowid.sqlite")
con.executemany("INSERT INTO sensor_nr VALUES (?,?,?,?)", rows)
con.commit()
con.close()
PY
