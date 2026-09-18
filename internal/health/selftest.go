package health

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// selfTestCase is one named check over a throwaway in-memory store.
type selfTestCase struct {
	name string
	run  func(db *sql.DB) error
}

// SelfTest runs the six contract checks with no filesystem or network:
// envelope parsing for a quantity and a category, an unknown metric
// folder, a tombstone arriving before its sample, a re-import producing
// zero new rows, and the day rollup. It returns one line per case.
func SelfTest() (string, bool) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return "self-test: cannot open memory store: " + err.Error(), false
	}
	defer db.Close()
	if _, err := db.Exec(Schema); err != nil {
		return "self-test: cannot create schema: " + err.Error(), false
	}
	cases := []selfTestCase{
		{"envelope-quantity", func(db *sql.DB) error {
			n, err := importLine(db, []byte(`{"uuid":"q1","metric":"steps","recordType":"q","start":"2026-09-01T08:00:00+02:00","end":"2026-09-01T08:01:00+02:00","localDate":"2026-09-01","timezone":"Europe/Amsterdam","value":300,"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-01T08:02:00+02:00","schemaVersion":1}`), "steps", false)
			if err != nil || n != 1 {
				return fmt.Errorf("quantity line new=%d err=%v, want 1 nil", n, err)
			}
			var got float64
			if err := db.QueryRow(`SELECT value_num FROM samples WHERE uuid='q1'`).Scan(&got); err != nil || got != 300 {
				return fmt.Errorf("quantity amount = %v err %v, want 300", got, err)
			}
			return nil
		}},
		{"envelope-category", func(db *sql.DB) error {
			n, err := importLine(db, []byte(`{"uuid":"c1","metric":"sleep","recordType":"c","start":"2026-09-01T23:00:00+02:00","end":"2026-09-02T00:30:00+02:00","localDate":"2026-09-01","timezone":"Europe/Amsterdam","value":{"code":4,"label":"deep sleep"},"unit":"","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-02T06:00:00+02:00","schemaVersion":1}`), "sleep", false)
			if err != nil || n != 1 {
				return fmt.Errorf("category line new=%d err=%v, want 1 nil", n, err)
			}
			var code int64
			var label string
			if err := db.QueryRow(`SELECT value_code, value_label FROM samples WHERE uuid='c1'`).Scan(&code, &label); err != nil || code != 4 || label != "deep sleep" {
				return fmt.Errorf("category split = (%d,%q) err %v, want (4,deep sleep)", code, label, err)
			}
			return nil
		}},
		{"unknown-metric", func(db *sql.DB) error {
			n, err := importLine(db, []byte(`{"uuid":"u1","recordType":"x","start":"2026-09-01T08:00:00+02:00","end":"2026-09-01T08:01:00+02:00","localDate":"2026-09-01","timezone":"Europe/Amsterdam","value":1,"unit":"u","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-01T08:02:00+02:00","schemaVersion":1}`), "never-seen", false)
			if err != nil || n != 1 {
				return fmt.Errorf("unknown metric new=%d err=%v, want 1 nil", n, err)
			}
			return nil
		}},
		{"early-tombstone", func(db *sql.DB) error {
			if _, err := importLine(db, []byte(`{"uuid":"gone","recordedAt":"2026-09-10T00:00:00+02:00"}`), "", true); err != nil {
				return err
			}
			n, err := importLine(db, []byte(`{"uuid":"gone","metric":"steps","recordType":"q","start":"2026-09-09T08:00:00+02:00","end":"2026-09-09T08:01:00+02:00","localDate":"2026-09-09","timezone":"Europe/Amsterdam","value":10,"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-09T08:02:00+02:00","schemaVersion":1}`), "steps", false)
			if err != nil {
				return err
			}
			_ = n
			var left int
			if err := db.QueryRow(`SELECT COUNT(*) FROM samples WHERE uuid='gone'`).Scan(&left); err != nil || left != 0 {
				return fmt.Errorf("sampled arriving after its tombstone survives: %d err %v", left, err)
			}
			return nil
		}},
		{"reimport-zero-new", func(db *sql.DB) error {
			line := []byte(`{"uuid":"r1","metric":"steps","recordType":"q","start":"2026-09-01T08:00:00+02:00","end":"2026-09-01T08:01:00+02:00","localDate":"2026-09-01","timezone":"Europe/Amsterdam","value":7,"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-01T08:02:00+02:00","schemaVersion":1}`)
			if _, err := importLine(db, line, "steps", false); err != nil {
				return err
			}
			n, err := importLine(db, line, "steps", false)
			if err != nil || n != 0 {
				return fmt.Errorf("re-import new=%d err=%v, want 0 nil", n, err)
			}
			return nil
		}},
		{"day-rollup", func(db *sql.DB) error {
			// Cases share one store and run in order: 2026-09-01 holds
			// q1 (300 steps) plus r1 (7 steps) by the time this runs.
			rows, err := db.Query(`SELECT local_date, SUM(value_num) FROM samples WHERE metric='steps' GROUP BY local_date ORDER BY local_date`)
			if err != nil {
				return err
			}
			defer rows.Close()
			days := map[string]float64{}
			for rows.Next() {
				var day string
				var total float64
				if err := rows.Scan(&day, &total); err != nil {
					return err
				}
				days[day] = total
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if days["2026-09-01"] != 307 {
				return fmt.Errorf("2026-09-01 steps = %v, want 307", days["2026-09-01"])
			}
			return nil
		}},
	}
	var lines []string
	ok := true
	for _, c := range cases {
		if err := c.run(db); err != nil {
			ok = false
			lines = append(lines, "FAIL "+c.name+": "+err.Error())
			continue
		}
		lines = append(lines, "ok "+c.name)
	}
	return "health self-test (" + strings.Join(lines, "; ") + ")", ok
}
