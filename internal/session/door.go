// Login door: supervised remote access to the resident browser's login
// screen, for signing in without SSH.
//
// The door opens only for a gone session, never to bypass an approval
// wait. It is one `icloud-mcp door` process (see ServeDoor) streaming the
// headless browser over CDP, which exits by itself at TTL even when no
// later tool call ever reaps the record. The record (pids, expiry, password file) is swept
// lazily by every session-tool entry, and early once a healthy session is
// observed, which is what a completed login looks like.
package session

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
)

// doorTTL is how long an opened door stays up. It matches the login
// wait: Apple 2FA plus typing, with slack, and no more.
const doorTTL = 20 * time.Minute

// doorPort is where the door prefers to listen, on loopback or the tailnet
// address; pickPort falls back to a free port when something else holds it.
const doorPort = "6080"

// doorProc is the door process's argv[0], what liveness checks match on.
const doorProc = "icloud-login-door"

// procFS is where process cmdlines and states are read for liveness
// checks. A variable so tests can point it at a fixture tree.
var procFS = "/proc"

// Door is one open login door, as recorded on disk.
type Door struct {
	Pid      int    `json:"pid"`
	Expires  int64  `json:"expires_unix"`
	PassFile string `json:"passfile"`
	URL      string `json:"url"`
	Via      string `json:"via"`
	Created  int64  `json:"created_unix"`

	// exited closes when this process saw the door exit; nil for a door
	// read back from its record.
	exited <-chan struct{}
}

func doorDir(state browser.State) string {
	return filepath.Join(state.Dir, "state", "login-door")
}

func doorRecord(state browser.State) string {
	return filepath.Join(doorDir(state), "door.json")
}

// tempPassword makes one door password from crypto/rand: 16 characters
// (about 93 bits) from an alphabet without look-alikes, so it survives
// being read off a phone. Rejection sampling, because the alphabet length
// is not a power of two.
func tempPassword() (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 16)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}

// doorURL builds the door link and the address its listener binds.
// A Tailscale IPv4 address turns the link into one that works from
// anywhere on the tailnet (the Telegram-remote case), and the listener
// binds exactly that address: tailnet members reach it, nothing public
// does. Anything else, including a tailscale that answers garbage, falls
// back to loopback, which needs the caller's own tunnel.
//
// ICLOUD_DOOR_BIND overrides the bind address for a container, whose own
// loopback the host cannot reach: bind 0.0.0.0 inside, publish the port
// on the host's loopback only (docker run -p 127.0.0.1:6080:6080), and
// the link is the host's loopback. It is ignored outside a container.
// Under `docker run --network host` the container shares the host's
// interfaces, so that setup is unsupported (SECURITY.md).
func doorURL() (url, via, bind string) {
	if b := os.Getenv("ICLOUD_DOOR_BIND"); b != "" && inContainer() {
		return "http://127.0.0.1:" + doorPort + "/", "published", b
	}
	if path, err := exec.LookPath("tailscale"); err == nil {
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if out, err := exec.CommandContext(ctx, path, "ip", "-4").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if ip := strings.TrimSpace(line); validIPv4(ip) {
					return "http://" + ip + ":" + doorPort + "/", "tailscale", ip
				}
			}
		}
	}
	return "http://127.0.0.1:" + doorPort + "/", "loopback", "127.0.0.1"
}

// containerMarkers are the files Docker and Podman put in every container.
// A variable so tests can point it at a fixture.
var containerMarkers = []string{"/.dockerenv", "/run/.containerenv"}

func inContainer() bool {
	for _, f := range containerMarkers {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

func validIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

// procExists reports whether pid names a living process.
func procExists(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// cmdline reads a process's argv; ok is false when it cannot be read.
func cmdline(pid int) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(procFS, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// procDead reports the zombie and dead process states.
func procDead(pid int) bool {
	stat, err := os.ReadFile(filepath.Join(procFS, strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	i := strings.LastIndex(string(stat), ")")
	if i < 0 {
		return false
	}
	state := strings.Fields(string(stat)[i+1:])
	if len(state) == 0 {
		return false
	}
	switch state[0] {
	case "Z", "X", "x", "K", "W":
		return true
	}
	return false
}

// procAlive reports whether pid is a living process running want. Signal 0
// alone trusts recycled pids and zombies, so on Linux the cmdline must
// name the expected binary and the state must not be zombie or dead.
// Anywhere without /proc it degrades to the signal check.
func procAlive(pid int, want string) bool {
	if !procExists(pid) {
		return false
	}
	raw, ok := cmdline(pid)
	if !ok {
		// No /proc to read (or no permission): assume alive rather than
		// reap a door that may well be serving.
		return true
	}
	// An empty cmdline is no name at all: a zombie, a kernel thread, or a
	// process still in the fork-to-exec window. Never a door server.
	if len(raw) == 0 || !strings.Contains(raw, want) {
		return false
	}
	return !procDead(pid)
}

// procIsOther reports whether pid is provably a different, living process
// than the one recorded: it exists and its cmdline is non-empty and does
// not name want. An empty or unreadable cmdline is NOT proof of a
// bystander, so this stays false and the caller may proceed. That matters
// for killGroup, where treating the fork-to-exec window as a mismatch
// silently declined to kill a door server it had just started.
func procIsOther(pid int, want string) bool {
	if !procExists(pid) {
		return false
	}
	raw, ok := cmdline(pid)
	if !ok || len(raw) == 0 {
		return false
	}
	return !strings.Contains(raw, want)
}

// killOne signals a single process, best-effort.
func killOne(pid int) {
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Kill()
	}
}

// liveDoor returns the recorded door when its processes still answer and
// its TTL has not passed.
func liveDoor(state browser.State) *Door {
	raw, err := os.ReadFile(doorRecord(state))
	if err != nil {
		return nil
	}
	var d Door
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	if time.Now().Unix() > d.Expires {
		return nil
	}
	if !procAlive(d.Pid, doorProc) {
		return nil
	}
	if _, err := os.Stat(d.PassFile); err != nil {
		return nil
	}
	return &d
}

// closeDoor kills the door process and shreds its password file. The pid
// is identity-checked first, so a recycled pid kills nothing innocent.
// Every step is best-effort: a dead process or missing file is the
// desired end state already.
func closeDoor(d *Door) {
	// A door this process started and saw exit is already gone, and its pid
	// may belong to another program by now (macOS has no cmdline to prove
	// otherwise), so it is never signalled.
	if d.exited != nil {
		select {
		case <-d.exited:
			shred(d.PassFile)
			return
		default:
		}
	}
	killGroup(d.Pid, doorProc)
	shred(d.PassFile)
}

// removeRecordFor drops the door record only when it still names pid: a
// newer door's record is never removed by an older door's cleanup.
func removeRecordFor(state browser.State, pid int) {
	raw, err := os.ReadFile(doorRecord(state))
	if err != nil {
		return
	}
	var d Door
	if json.Unmarshal(raw, &d) == nil && d.Pid == pid {
		_ = os.Remove(doorRecord(state))
	}
}

// killGroup kills a process group whose leader is the recorded door
// process, so nothing the door started outlives it.
//
// The gate is deliberately "kill unless we can prove this is somebody
// else", not "kill only if the cmdline matches". A pid in its fork-to-exec
// window has an empty cmdline, and requiring a match there silently
// declined to kill a door server that had just started, leaving it
// running until its TTL. Only a non-empty cmdline naming a different
// program proves a recycled pid, and that is the one case we skip.
//
// Two more guards: a pid that is not its own group leader gets a direct
// kill (there is no group of its own to take down), and our own process
// group is never signalled, however the record came to name it.
func killGroup(pid int, want string) {
	if pid <= 0 || !procExists(pid) {
		return
	}
	if procIsOther(pid, want) {
		return
	}
	ourGroup := syscall.Getpgrp()
	if pgid, err := syscall.Getpgid(pid); err != nil || pgid != pid || pgid == ourGroup {
		killOne(pid)
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	if procAlive(pid, want) {
		killOne(pid)
	}
}

// shred overwrites a secret file once before removing it.
func shred(path string) {
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		junk := make([]byte, info.Size())
		_, _ = rand.Read(junk)
		_ = os.WriteFile(path, junk, 0o600)
	}
	_ = os.Remove(path)
}

// ReapDoors closes expired doors, and every door once a signed-in session
// is observed (healthy or waiting for approval: either way the sign-in
// the door was opened for has happened, and the grant is reask_access's
// job). It returns one line per closed door, like the tab reaper, and
// nothing when there is nothing to say.
func ReapDoors(state browser.State) []string {
	if out := sweepExpired(state); out != nil {
		return out
	}
	raw, err := os.ReadFile(doorRecord(state))
	if err != nil {
		return nil
	}
	var d Door
	if err := json.Unmarshal(raw, &d); err != nil {
		_ = os.Remove(doorRecord(state))
		return []string{"removed an unreadable login-door record"}
	}
	switch outcome, _ := browser.NewCDP(state.CDP).Check(); outcome {
	case browser.OK, browser.NeedsApproval:
		closeDoor(&d)
		_ = os.Remove(doorRecord(state))
		return []string{"closed the login door: a sign-in was observed"}
	}
	return nil
}

// sweepExpired closes doors past TTL without touching the browser, so
// even pure-probe entries sweep. It returns nil when no record exists or
// the recorded door is still live.
func sweepExpired(state browser.State) []string {
	raw, err := os.ReadFile(doorRecord(state))
	if err != nil {
		return nil
	}
	var d Door
	if err := json.Unmarshal(raw, &d); err != nil {
		_ = os.Remove(doorRecord(state))
		return []string{"removed an unreadable login-door record"}
	}
	if time.Now().Unix() <= d.Expires && liveDoor(state) != nil {
		return nil
	}
	// liveDoor is nil for expired records and for dead processes alike;
	// closeDoor is best-effort either way, so a dead door never sits on
	// its secret until TTL waiting for traffic.
	expired := time.Now().Unix() > d.Expires
	closeDoor(&d)
	_ = os.Remove(doorRecord(state))
	if expired {
		return []string{"closed the expired login door"}
	}
	return []string{"removed a dead login door"}
}

// writeRecord stores the door atomically, so a crash cannot leave a
// half-written record pointing at pids nobody owns.
func writeRecord(state browser.State, d *Door) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tmp := doorRecord(state) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, doorRecord(state))
}

// startDoor opens a fresh login door: temp password, one door process
// serving the resident browser over CDP, record on disk. On any failure it
// undoes the partial start. The door exits at TTL on its own, so it dies
// even if no later tool call ever reaps the record.
func startDoor(state browser.State) (*Door, string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, "", fmt.Errorf("cannot find this binary to run the door: %v", err)
	}
	pass, err := tempPassword()
	if err != nil {
		return nil, "", fmt.Errorf("could not make a door password: %v", err)
	}
	dir := doorDir(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	_ = os.Chmod(dir, 0o700)
	// 0600 from creation, read once by the door and shredded on close;
	// the password never travels in argv.
	// One file per door: a door that outlives its replacement shreds its
	// own password on exit, never the new door's. Any file left from a
	// door that died without cleaning up (or the old fixed name) is
	// shredded here, since the door being replaced is already closed.
	if old, _ := filepath.Glob(filepath.Join(dir, "doorpass*")); len(old) > 0 {
		for _, f := range old {
			shred(f)
		}
	}
	passFile := filepath.Join(dir, fmt.Sprintf("doorpass-%d", time.Now().UnixNano()))
	if err := os.WriteFile(passFile, []byte(pass), 0o600); err != nil {
		return nil, "", err
	}
	logPath := filepath.Join(dir, "door.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		shred(passFile)
		return nil, "", err
	}
	defer logFile.Close()
	link, via, bind := doorURL()
	port := doorPort // a published port must be the one the host maps
	if via != "published" {
		port = pickPort(bind)
	}
	link = strings.Replace(link, ":"+doorPort+"/", ":"+port+"/", 1)
	addr := net.JoinHostPort(bind, port)
	ttl := strconv.FormatInt(int64(doorTTL/time.Second), 10)
	cmd := exec.Command(exe, "door", addr, ttl, passFile)
	cmd.Args[0] = doorProc
	cmd.Env = append(os.Environ(), "ICLOUD_CDP="+state.CDP)
	// Own process group, so group-kill in closeDoor never takes the caller.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		shred(passFile)
		return nil, "", fmt.Errorf("could not start the door: %v", err)
	}
	// Reap it whenever it exits, so it never lingers as a zombie under
	// this long-lived server.
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	undo := func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		shred(passFile)
	}
	// The port answering is not enough: in published mode the port is
	// fixed, and a door being replaced can still hold it for a moment.
	if !listening(addr, 3*time.Second) || !procAlive(cmd.Process.Pid, doorProc) {
		undo()
		out, _ := os.ReadFile(logPath)
		return nil, "", fmt.Errorf("the door did not come up on %s: %s", addr, strings.TrimSpace(string(out)))
	}
	now := time.Now()
	d := &Door{
		Pid:      cmd.Process.Pid,
		Expires:  now.Add(doorTTL).Unix(),
		PassFile: passFile,
		URL:      link,
		Via:      via,
		Created:  now.Unix(),
		exited:   exited,
	}
	if err := writeRecord(state, d); err != nil {
		undo()
		return nil, "", err
	}
	return d, pass, nil
}

// pickPort is doorPort when it is free on bind, else any free port.
func pickPort(bind string) string {
	for _, want := range []string{doorPort, "0"} {
		if l, err := net.Listen("tcp", net.JoinHostPort(bind, want)); err == nil {
			_, port, _ := net.SplitHostPort(l.Addr().String())
			_ = l.Close()
			return port
		}
	}
	return doorPort
}

// listening reports whether addr accepts a TCP connection within wait.
func listening(addr string, wait time.Duration) bool {
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return true
		}
	}
	return false
}

// replaceDoor closes any open door and opens a fresh one, so every open
// issues a new password and an old one stops working at once.
func replaceDoor(state browser.State) (*Door, string, error) {
	if raw, err := os.ReadFile(doorRecord(state)); err == nil {
		var old Door
		if json.Unmarshal(raw, &old) == nil {
			closeDoor(&old)
			// Wait for it to go: in published mode the port is fixed, and
			// a dying door still answering there would pass the new door's
			// readiness check before the new one had bound.
			for i := 0; i < 30 && old.Pid > 0 && procAlive(old.Pid, doorProc); i++ {
				time.Sleep(100 * time.Millisecond)
			}
			removeRecordFor(state, old.Pid)
		} else {
			_ = os.Remove(doorRecord(state)) // unreadable: nobody's
		}
	}
	return startDoor(state)
}

// OpenDoor is replaceDoor for the operator's `icloud-mcp login`: no ask,
// because whoever runs it on the host is the owner. It returns the door,
// its one-time password, and a close func that kills it and drops the
// record.
func OpenDoor(state browser.State) (*Door, string, func(), error) {
	// The same lock open_login holds, so the two never replace each
	// other's door halfway.
	unlock, err := browser.AppLock(state.Dir, "login-door", 60*time.Second)
	if err != nil {
		return nil, "", nil, fmt.Errorf("another login-door operation is still running")
	}
	defer unlock()
	d, pass, err := replaceDoor(state)
	if err != nil {
		return nil, "", nil, err
	}
	return d, pass, func() { closeDoor(d); removeRecordFor(state, d.Pid) }, nil
}
