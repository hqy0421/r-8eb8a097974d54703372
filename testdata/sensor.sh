#!/bin/bash
set -eu

# A table with a large attachment that spans many overflow pages, while short
# fields (device_no, length) sit on the same record and other short fields
# (note, reading) are stored after the attachment. The small page size makes
# even modest values overflow.
rm -f sensor.sqlite
sqlite3 --batch sensor.sqlite <<'HERE'
PRAGMA page_size=512;
CREATE TABLE sensor (
    device_no  TEXT NOT NULL,
    length     INTEGER,
    attachment BLOB,
    note       TEXT,
    reading    REAL
);
CREATE INDEX sensor_length ON sensor (length);
HERE

python3 -I - <<'PY'
import sqlite3

rows = [("dev-001", 12, None, "no attachment", 3.5)]
for i in range(2, 8):
    blob = bytes((j * 7 + i) & 0xff for j in range(8000 + i * 137))
    rows.append((f"dev-{i:03d}", 1000 + i, blob, f"note-{i}", 20.5 + i))

con = sqlite3.connect("sensor.sqlite")
con.executemany("INSERT INTO sensor VALUES (?,?,?,?,?)", rows)

# a TEXT attachment on the overflow chain as well
big_text = ("sensor-reading-" * 700)[:9000]
con.execute(
    "INSERT INTO sensor VALUES (?,?,?,?,?)",
    ("dev-text", 4242, big_text.encode(), "tail-note", 9.75),
)
con.commit()
con.close()
PY
