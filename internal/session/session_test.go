package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
	"github.com/sjdonado/icloud-headless-mcp/internal/config"
)

func TestTempPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		p, err := tempPassword()
		if err != nil {
			t.Fatalf("tempPassword: %v", err)
		}
		// Exactly 8: classic VNC auth truncates longer passwords silently.
		if len(p) != 8 {
			t.Fatalf("password %q is %d chars, want exactly 8", p, len(p))
		}
		for _, r := range p {
			if !strings.ContainsRune("abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789", r) {
				t.Fatalf("password %q holds %q outside the unambiguous alphabet", p, r)
			}
		}
		seen[p] = true
	}
	if len(seen) < 10 {
		t.Fatal("passwords repeat: rand source suspect")
	}
}

func TestQuietRefusal(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Skip("no zoneinfo")
	}
	at := func(h, m int) time.Time {
		return time.Date(2026, time.January, 15, h, m, 0, 0, loc)
	}
	for _, tc := range []struct {
		when time.Time
		want bool
	}{
		{at(22, 59), false},
		{at(23, 0), true},
		{at(3, 0), true},
		{at(6, 59), true},
		{at(7, 0), false},
		{at(12, 0), false},
	} {
		msg := quietRefusal(loc, tc.when, "Reminders")
		if (msg != "") != tc.want {
			t.Errorf("%s: refusal=%v, want refusal=%v", tc.when.Format("15:04"), msg != "", tc.want)
		}
	}
}

func testState(t *testing.T) (browser.State, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	shared := t.TempDir()
	cfg := &config.Config{
		StateDir: dir, SharedState: shared,
		CDP:     "http://127.0.0.1:9",
		AgentTZ: "Europe/Amsterdam",
	}
	return browser.State{Dir: dir, Shared: shared, CDP: cfg.CDP}, cfg
}

func TestReapDoorsEmpty(t *testing.T) {
	state, _ := testState(t)
	if out := ReapDoors(state); out != nil {
		t.Fatalf("no record should reap nothing, got %v", out)
	}
}

func writeRawRecord(t *testing.T, state browser.State, vncPid, novncPid int, expires int64, passfile string) {
	t.Helper()
	if err := os.MkdirAll(doorDir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{"vnc_pid":` + itoa(vncPid) + `,"novnc_pid":` + itoa(novncPid) +
		`,"expires_unix":` + itoa64(expires) + `,"passfile":` + quoted(passfile) +
		`,"url":"http://127.0.0.1:6080/vnc.html","via":"loopback","created_unix":` + itoa64(expires-1) + `}`
	if err := os.WriteFile(doorRecord(state), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

// startNamed starts a sleeper whose cmdline contains name as a substring,
// the property procAlive matches on (production door pids are timeout
// wrappers whose cmdlines contain the server name deeper in). Group
// semantics themselves are covered by TestKillGroupTakesWholeGroup, so
// this only shapes argv.
func startNamed(t *testing.T, argv0 string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.Args[0] = argv0
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("cannot start liveness fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func TestLiveDoorAndExpiredReap(t *testing.T) {
	state, _ := testState(t)
	passfile := filepath.Join(t.TempDir(), "vncpass")
	if err := os.WriteFile(passfile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Live processes shaped like the door servers; expiry in the future.
	vnc := startNamed(t, "x11vnc")
	novnc := startNamed(t, "websockify")
	writeRawRecord(t, state, vnc.Process.Pid, novnc.Process.Pid, time.Now().Add(time.Hour).Unix(), passfile)
	if d := liveDoor(state); d == nil {
		t.Fatal("recorded live door reads back dead")
	}
	// Expired: reap closes, shreds, and removes without touching CDP.
	writeRawRecord(t, state, 1<<30, 1<<30, time.Now().Add(-time.Minute).Unix(), passfile)
	out := ReapDoors(state)
	if len(out) != 1 || !strings.Contains(out[0], "expired") {
		t.Fatalf("expired reap = %v, want one expired line", out)
	}
	if _, err := os.Stat(doorRecord(state)); !os.IsNotExist(err) {
		t.Fatal("record survives its reap")
	}
	if _, err := os.Stat(passfile); !os.IsNotExist(err) {
		t.Fatal("password file survives its reap")
	}
}

func TestNovncURLLoopbackWithoutTailscale(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	url, via, bind := novncURL()
	if via != "loopback" || bind != "127.0.0.1" || url != "http://127.0.0.1:6080/vnc.html" {
		t.Fatalf("no tailscale should mean loopback, got %q via %q", url, via)
	}
}

func TestReaskAccessLatchClearRefusesBeforeAsk(t *testing.T) {
	state, cfg := testState(t)
	asked := false
	res, rerr := reaskAccess(context.Background(), cfg, state, func(context.Context, string) string {
		asked = true
		return ""
	})
	m := resultMap(t, res, rerr)
	if m["reasked"] != false || asked {
		t.Fatalf("clear latch should refuse before asking: %v asked=%v", m, asked)
	}
}

func TestReaskCoreNoBrowser(t *testing.T) {
	_, cfg := testState(t)
	msg, code := Reask(cfg)
	if code != 2 || !strings.Contains(msg, "browser") {
		t.Fatalf("no browser should be code 2 naming the browser: code=%d msg=%q", code, msg)
	}
}

func TestProcAlive(t *testing.T) {
	root := t.TempDir()
	old := procFS
	procFS = root
	defer func() { procFS = old }()
	mkproc := func(pid, cmdline, stat string) {
		dir := filepath.Join(root, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	me := os.Getpid()
	mkproc(itoa(me), "x11vnc -display :99\x00", "1 (x11vnc) R 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1 0 0 0 0 0 0")
	if !procAlive(me, "x11vnc") {
		t.Fatal("matching live process should be alive")
	}
	if procAlive(me, "websockify") {
		t.Fatal("cmdline mismatch should not be alive")
	}
	mkproc(itoa(me+1), "x11vnc -display :99\x00", "1 (x11vnc) Z 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1 0 0 0 0 0 0")
	// pid me+1 may or may not exist as a real process; the zombie fixture
	// only matters when the signal check passes, so assert the mismatch
	// path instead: wrong binary never passes.
	if procAlive(me+1, "definitely-not-this-binary-name") {
		t.Fatal("cmdline mismatch should never pass")
	}
	if procAlive(1<<30, "x11vnc") {
		t.Fatal("nonexistent pid should not be alive")
	}
}

func TestNovncURLRejectsGarbageTailscale(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'not-an-ip'\n"
	if err := os.WriteFile(filepath.Join(dir, "tailscale"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	url, via, bind := novncURL()
	if via != "loopback" || bind != "127.0.0.1" || url != "http://127.0.0.1:6080/vnc.html" {
		t.Fatalf("garbage tailscale output should mean loopback, got %q %q %q", url, via, bind)
	}
}

func TestOpenLoginUnreachableRefusesWithoutAsk(t *testing.T) {
	state, cfg := testState(t)
	asked := false
	res, rerr := openLogin(context.Background(), cfg, state, func(context.Context, string) string {
		asked = true
		return ""
	})
	m := resultMap(t, res, rerr)
	if m["door"] != "unavailable" || asked {
		t.Fatalf("unreachable browser should refuse before asking: %v asked=%v", m, asked)
	}
}

func TestOpenLoginLatchedRoutesToReask(t *testing.T) {
	state, cfg := testState(t)
	latch := filepath.Join(state.Shared, "blocked")
	if err := os.MkdirAll(state.Shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(latch, []byte("test latch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	asked := false
	res, rerr := openLogin(context.Background(), cfg, state, func(context.Context, string) string {
		asked = true
		return ""
	})
	m := resultMap(t, res, rerr)
	if m["door"] != "not-needed" || asked {
		t.Fatalf("latched grant should route to reask_access: %v asked=%v", m, asked)
	}
	if msg, _ := m["error"].(string); !strings.Contains(msg, "reask_access") {
		t.Fatalf("routing should name reask_access: %q", msg)
	}
}

func TestKillGroupTakesWholeGroup(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skip("no sleep binary")
	}
	started := time.Now()
	killGroup(cmd.Process.Pid, "sleep")
	_ = cmd.Wait()
	// A missed kill window shows up as the sleeper's full lifetime with a
	// vacuous pass; fail loudly instead so the suite stays honest.
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("kill took %v: the process outlived its killer", elapsed)
	}
	if procAlive(cmd.Process.Pid, "sleep") {
		t.Fatal("group kill left the process alive")
	}
}

func TestSweepExpiredRemovesDeadDoor(t *testing.T) {
	state, _ := testState(t)
	passfile := filepath.Join(t.TempDir(), "vncpass")
	if err := os.WriteFile(passfile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Unexpired record, dead processes: the secret must not sit until TTL.
	writeRawRecord(t, state, 1<<30, 1<<30, time.Now().Add(time.Hour).Unix(), passfile)
	out := sweepExpired(state)
	if len(out) != 1 || !strings.Contains(out[0], "dead") {
		t.Fatalf("dead door should sweep with one dead line, got %v", out)
	}
	if _, err := os.Stat(doorRecord(state)); !os.IsNotExist(err) {
		t.Fatal("dead record survives its sweep")
	}
	if _, err := os.Stat(passfile); !os.IsNotExist(err) {
		t.Fatal("dead door password file survives its sweep")
	}
}

func TestLiveDoorVerifiesCmdline(t *testing.T) {
	state, _ := testState(t)
	passfile := filepath.Join(t.TempDir(), "vncpass")
	if err := os.WriteFile(passfile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Fixture /proc keyed by two alive pids (self plus parent): the
	// match/mismatch decision is then platform-independent, unlike the
	// real-process case which is vacuous where /proc is absent.
	root := t.TempDir()
	old := procFS
	procFS = root
	defer func() { procFS = old }()
	writeProc := func(pid int, cmdline, stat string) {
		dir := filepath.Join(root, itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stat := "1 (test) S 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1 0 0 0 0 0 0"
	me, parent := os.Getpid(), os.Getppid()
	writeProc(me, "test binary\x00", stat)
	writeProc(parent, "test runner\x00", stat)
	writeRawRecord(t, state, me, parent, time.Now().Add(time.Hour).Unix(), passfile)
	if d := liveDoor(state); d != nil {
		t.Fatal("cmdline mismatch should read the door dead")
	}
	writeProc(me, "timeout 1200s /usr/bin/x11vnc -display :99\x00", stat)
	writeProc(parent, "python3 /usr/bin/websockify --web=/usr/share/novnc\x00", stat)
	if d := liveDoor(state); d == nil {
		t.Fatal("cmdline match should read the door live")
	}
}

// TestKillGroupSurvivesExecWindow is the regression for the CI failure: a
// pid killed immediately after Start sits in its fork-to-exec window,
// where /proc/PID/cmdline reads empty. Gating on a cmdline match there
// declined the kill and the sleeper outlived its killer.
func TestKillGroupSurvivesExecWindow(t *testing.T) {
	for i := 0; i < 25; i++ {
		cmd := exec.Command("sleep", "30")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatalf("iteration %d: start: %v", i, err)
		}
		started := time.Now()
		killGroup(cmd.Process.Pid, "sleep")
		_ = cmd.Wait()
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("iteration %d: kill took %v, the sleeper outlived its killer", i, elapsed)
		}
	}
}

// TestKillGroupSparesRecycledPid proves the one case the gate still skips:
// a living process whose cmdline names a different program.
func TestKillGroupSparesRecycledPid(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.Args[0] = "innocent-bystander"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	killGroup(cmd.Process.Pid, "x11vnc")
	time.Sleep(100 * time.Millisecond)
	if !procExists(cmd.Process.Pid) {
		t.Fatal("killGroup killed a process whose cmdline named another program")
	}
}
