package health

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
)

// Rollup inputs, resolved against whatever metric names the store holds.
// Folder names vary by exporter version, so resolution is token matching
// over the distinct stored names. One key can claim several names (a
// renamed folder splits history across two names); every claimed name is
// summed. Anything unmatched is a named blind spot, never a silent zero.
var metricTokens = []struct {
	key    string
	tokens []string
}{
	{"sleep", []string{"sleep"}},
	{"resting", []string{"restingheartrate", "restingheart", "restinghr", "resting"}},
	{"hrv", []string{"hrv", "heartratevariability"}},
	{"steps", []string{"step"}},
	{"energy", []string{"activeenergy", "energy"}},
	{"hr", []string{"heartrate"}},
}

// norm folds a metric name the same way for stored names and tokens:
// lowercased, spaces and underscores gone.
func norm(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "_", ""))
}

func resolveMetrics(db *sql.DB) (map[string][]string, []string, error) {
	rows, err := db.Query(`SELECT DISTINCT metric FROM samples`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, nil, err
		}
		names = append(names, m)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	sort.Strings(names)
	claimed := map[string]bool{}
	resolved := map[string][]string{}
	for _, want := range metricTokens {
		for _, n := range names {
			if claimed[n] {
				continue
			}
			folded := norm(n)
			for _, tok := range want.tokens {
				if strings.Contains(folded, tok) {
					resolved[want.key] = append(resolved[want.key], n)
					claimed[n] = true
					break
				}
			}
		}
	}
	return resolved, names, nil
}

// inClause renders metrics as a parameter list for IN (...).
func inClause(metrics []string) (string, []any) {
	holders := make([]string, len(metrics))
	args := make([]any, len(metrics))
	for i, m := range metrics {
		holders[i] = "?"
		args[i] = m
	}
	return strings.Join(holders, ","), args
}

func sumDay(db *sql.DB, metrics []string, day string) (any, string) {
	ph, args := inClause(metrics)
	args = append(args, day)
	var total sql.NullFloat64
	err := db.QueryRow(`SELECT SUM(value_num) FROM samples WHERE metric IN (`+ph+`) AND local_date=? AND value_type='quantity'`, args...).Scan(&total)
	if err != nil {
		return nullReason(err.Error())
	}
	if !total.Valid {
		return nil, ""
	}
	return total.Float64, ""
}

func minDay(db *sql.DB, metrics []string, day string) (any, string) {
	ph, args := inClause(metrics)
	args = append(args, day)
	var v sql.NullFloat64
	err := db.QueryRow(`SELECT MIN(value_num) FROM samples WHERE metric IN (`+ph+`) AND local_date=? AND value_type='quantity'`, args...).Scan(&v)
	if err != nil {
		return nullReason(err.Error())
	}
	if !v.Valid {
		return nil, ""
	}
	return v.Float64, ""
}

func meanDay(db *sql.DB, metrics []string, day string) (any, string) {
	ph, args := inClause(metrics)
	args = append(args, day)
	var v sql.NullFloat64
	err := db.QueryRow(`SELECT AVG(value_num) FROM samples WHERE metric IN (`+ph+`) AND local_date=? AND value_type='quantity'`, args...).Scan(&v)
	if err != nil {
		return nullReason(err.Error())
	}
	if !v.Valid {
		return nil, ""
	}
	return v.Float64, ""
}

// Status reports coverage, freshness, and blind spots: every metric ever
// seen with its span, every expected-but-absent metric named with its
// reason, unapplied tombstones counted, and the last import run described.
func Status(cfg *config.Config) map[string]any {
	db, out, ok := openQueryDB(cfg)
	if !ok {
		return out
	}
	defer db.Close()
	metrics := map[string]any{}
	rows, err := db.Query(`SELECT metric, COUNT(*), MIN(local_date), MAX(local_date), MAX(recorded_at) FROM samples GROUP BY metric`)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	present := 0
	for rows.Next() {
		var m, first, last, recorded string
		var n int
		if err := rows.Scan(&m, &n, &first, &last, &recorded); err != nil {
			rows.Close()
			return map[string]any{"error": err.Error()}
		}
		present++
		metrics[m] = map[string]any{"samples": n, "first_date": first, "last_date": last, "latest_recorded_at": recorded}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return map[string]any{"error": err.Error()}
	}
	rows.Close()
	resolved, _, err := resolveMetrics(db)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	var missing []map[string]any
	for _, want := range metricTokens {
		if len(resolved[want.key]) == 0 {
			missing = append(missing, map[string]any{"metric": want.key, "reason": "no samples imported"})
		}
	}
	if missing == nil {
		missing = []map[string]any{}
	}
	var unapplied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tombstones WHERE applied=0`).Scan(&unapplied); err != nil {
		return map[string]any{"error": err.Error()}
	}
	var lastImport map[string]any
	var fname, fmetric, at string
	var seen, fresh int
	if err := db.QueryRow(`SELECT file_name, metric, rows_seen, rows_new, imported_at FROM imports ORDER BY id DESC LIMIT 1`).Scan(&fname, &fmetric, &seen, &fresh, &at); err == nil {
		lastImport = map[string]any{"file": fname, "metric": fmetric, "rows_seen": seen, "rows_new": fresh, "at": at}
	}
	return map[string]any{
		"metrics":               metrics,
		"missing":               missing,
		"unapplied_tombstones":  unapplied,
		"last_import":           lastImport,
		"metrics_present_count": present,
	}
}

// Days rolls up steps, active energy, resting heart rate, and HRV per day.
// A metric that errored is null with its reason, never zero; a day with
// no samples at all carries nulls with no error attached.
func Days(cfg *config.Config, n int) map[string]any {
	db, out, ok := openQueryDB(cfg)
	if !ok {
		return out
	}
	defer db.Close()
	today, err := ownerToday(cfg)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	resolved, _, err := resolveMetrics(db)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	var days []map[string]any
	for _, day := range lastNDays(today, n) {
		row := map[string]any{"date": day}
		setMetricOrNull(row, "steps", resolved["steps"], func(m []string) (any, string) { return sumDay(db, m, day) })
		setMetricOrNull(row, "active_energy", resolved["energy"], func(m []string) (any, string) { return sumDay(db, m, day) })
		setMetricOrNull(row, "resting_bpm", resolved["resting"], func(m []string) (any, string) { return minDay(db, m, day) })
		setMetricOrNull(row, "hrv", resolved["hrv"], func(m []string) (any, string) { return meanDay(db, m, day) })
		days = append(days, row)
	}
	return map[string]any{"days": days, "window_days": n}
}

func setMetricOrNull(row map[string]any, field string, metrics []string, f func([]string) (any, string)) {
	if len(metrics) == 0 {
		row[field] = nil
		return
	}
	v, reason := f(metrics)
	withReason(row, field, v, reason)
}

func withReason(out map[string]any, field string, v any, reason string) {
	out[field] = v
	if reason != "" {
		out[field+"_error"] = reason
	}
}

// Sleep groups sleep segments into nights keyed by onset localDate, which
// stays authoritative even when timestamps mix UTC offsets. A new night
// starts after a gap longer than three hours; overlapping or duplicate
// segments merge into the open night. Stage labels are preserved per
// night with per-stage minutes, and skipped segments are counted, never
// silently dropped.
func Sleep(cfg *config.Config, windowNights int) map[string]any {
	db, out, ok := openQueryDB(cfg)
	if !ok {
		return out
	}
	defer db.Close()
	today, err := ownerToday(cfg)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	resolved, _, err := resolveMetrics(db)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	m := resolved["sleep"]
	if len(m) == 0 {
		return map[string]any{"nights": []map[string]any{}, "window_nights": windowNights,
			"note": "no sleep metric imported"}
	}
	cutoff, err := time.Parse("2006-01-02", today)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	ph, args := inClause(m)
	args = append(args, cutoff.AddDate(0, 0, -windowNights-2).Format("2006-01-02"))
	rows, err := db.Query(`SELECT start_at, end_at, value_label, local_date FROM samples WHERE metric IN (`+ph+`) AND value_type='category' AND local_date >= ? ORDER BY datetime(start_at)`, args...)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	defer rows.Close()
	type night struct {
		date   string
		onset  time.Time
		wake   time.Time
		stages map[string]float64
		last   time.Time
	}
	var episodes []*night
	skipped := 0
	for rows.Next() {
		var s, e, label, day string
		if err := rows.Scan(&s, &e, &label, &day); err != nil {
			return map[string]any{"error": err.Error()}
		}
		start, err := time.Parse(time.RFC3339, s)
		if err != nil {
			skipped++
			continue
		}
		end := start
		if parsed, err := time.Parse(time.RFC3339, e); err == nil && parsed.After(start) {
			end = parsed
		}
		if label == "" {
			label = "unknown"
		}
		if day == "" {
			day = start.Format("2006-01-02")
		}
		// Attach to the latest open episode, including overlapping or
		// duplicate segments (never a second night for the same sleep).
		// Same localDate never merges two episodes: a nap and a night
		// stay separate entries sharing a date.
		attached := false
		if len(episodes) > 0 {
			nt := episodes[len(episodes)-1]
			if !start.After(nt.last.Add(3 * time.Hour)) {
				nt.stages[label] += end.Sub(start).Minutes()
				if end.After(nt.wake) {
					nt.wake = end
				}
				if end.After(nt.last) {
					nt.last = end
				}
				attached = true
			}
		}
		if !attached {
			episodes = append(episodes, &night{
				date: day, onset: start, wake: end,
				stages: map[string]float64{label: end.Sub(start).Minutes()},
				last:   end,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return map[string]any{"error": err.Error()}
	}
	var res []map[string]any
	for i := len(episodes) - 1; i >= 0 && len(res) < windowNights; i-- {
		nt := episodes[i]
		total := 0.0
		stages := map[string]any{}
		for label, mins := range nt.stages {
			stages[label] = mins
			total += mins
		}
		res = append(res, map[string]any{
			"night": nt.date, "onset": nt.onset.Format(time.RFC3339),
			"wake":          nt.wake.Format(time.RFC3339),
			"total_minutes": total, "stages": stages,
		})
	}
	if res == nil {
		res = []map[string]any{}
	}
	return map[string]any{"nights": res, "window_nights": windowNights, "skipped_segments": skipped}
}

// Effort sums minutes at or above a heart-rate floor per day. A day with
// no heart samples at all is null, which reads differently from a
// measured zero; unparsable samples are counted, never silently dropped.
func Effort(cfg *config.Config, n, floor int) map[string]any {
	db, out, ok := openQueryDB(cfg)
	if !ok {
		return out
	}
	defer db.Close()
	today, err := ownerToday(cfg)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	resolved, _, err := resolveMetrics(db)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	m := resolved["hr"]
	if len(m) == 0 {
		return map[string]any{"days": []map[string]any{}, "floor_bpm": floor, "window_days": n,
			"note": "no heart-rate metric imported"}
	}
	ph, args := inClause(m)
	var days []map[string]any
	for _, day := range lastNDays(today, n) {
		qargs := append(append([]any{}, args...), day)
		rows, err := db.Query(`SELECT start_at, end_at, value_num FROM samples WHERE metric IN (`+ph+`) AND local_date=? AND value_type='quantity'`, qargs...)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		minutes := 0.0
		samples := 0
		skipped := 0
		for rows.Next() {
			var s, e string
			var v float64
			if err := rows.Scan(&s, &e, &v); err != nil {
				rows.Close()
				return map[string]any{"error": "could not read heart-rate samples"}
			}
			samples++
			if v < float64(floor) {
				continue
			}
			start, err1 := time.Parse(time.RFC3339, s)
			end, err2 := time.Parse(time.RFC3339, e)
			if err1 != nil || err2 != nil || !end.After(start) {
				skipped++
				continue
			}
			minutes += end.Sub(start).Minutes()
		}
		rows.Close()
		row := map[string]any{"date": day}
		if samples == 0 {
			row["minutes_above_floor"] = nil
		} else {
			row["minutes_above_floor"] = minutes
		}
		row["skipped_samples"] = skipped
		days = append(days, row)
	}
	return map[string]any{"days": days, "floor_bpm": floor, "window_days": n}
}

// Recovery compares recent-window means against baseline means for resting
// heart rate and HRV. Resting compares daily minimums, the same number
// health_days reports, so the two tools cannot disagree on a day. Windows
// with fewer than three covered days report null with the reason instead
// of a mean over scraps.
func Recovery(cfg *config.Config, recent, baseline int) map[string]any {
	db, out, ok := openQueryDB(cfg)
	if !ok {
		return out
	}
	defer db.Close()
	today, err := ownerToday(cfg)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	resolved, _, err := resolveMetrics(db)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	meanOver := func(metrics []string, days []string, daily func(*sql.DB, []string, string) (any, string)) (any, string, int) {
		if len(metrics) == 0 {
			return nil, "", 0
		}
		var vals []float64
		for _, day := range days {
			v, reason := daily(db, metrics, day)
			if reason != "" {
				return nil, reason, len(vals)
			}
			if v == nil {
				continue
			}
			f, ok := v.(float64)
			if !ok {
				continue
			}
			vals = append(vals, f)
		}
		if len(vals) < 3 {
			return nil, fmt.Sprintf("only %d of %d days covered", len(vals), len(days)), len(vals)
		}
		sum := 0.0
		for _, v := range vals {
			sum += v
		}
		return sum / float64(len(vals)), "", len(vals)
	}
	all := lastNDays(today, baseline)
	recentDays := all
	if recent < len(all) {
		recentDays = all[len(all)-recent:]
	}
	compare := func(metrics []string, daily func(*sql.DB, []string, string) (any, string)) map[string]any {
		r, rErr, rN := meanOver(metrics, recentDays, daily)
		b, bErr, bN := meanOver(metrics, all, daily)
		m := map[string]any{"recent": r, "baseline": b, "recent_days": rN, "baseline_days": bN}
		if rErr != "" {
			m["recent_error"] = rErr
		}
		if bErr != "" {
			m["baseline_error"] = bErr
		}
		if r != nil && b != nil {
			m["delta"] = r.(float64) - b.(float64)
		} else {
			m["delta"] = nil
		}
		return m
	}
	outRecovery := map[string]any{"recent_days": recent, "baseline_days": baseline}
	if m := resolved["resting"]; len(m) > 0 {
		outRecovery["resting_bpm"] = compare(m, minDay)
	} else {
		outRecovery["resting_bpm"] = map[string]any{"recent": nil, "baseline": nil, "delta": nil,
			"note": "no resting heart-rate metric imported"}
	}
	if m := resolved["hrv"]; len(m) > 0 {
		outRecovery["hrv"] = compare(m, meanDay)
	} else {
		outRecovery["hrv"] = map[string]any{"recent": nil, "baseline": nil, "delta": nil,
			"note": "no HRV metric imported"}
	}
	return outRecovery
}
