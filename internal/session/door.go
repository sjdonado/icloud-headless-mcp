// Login door: supervised VNC access for signing in without SSH.
//
// The door opens only for a gone session, never to bypass an approval
// wait. It is one x11vnc plus one loopback websockify, both wrapped in
// `timeout` so the processes die at TTL even when no later tool call ever
// reaps the record. The record (pids, expiry, password file) is swept
// lazily by every session-tool entry, and early once a healthy session is
// observed, which is what a completed login looks like.
package session

import (
	"bytes"
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

const (
	vncPort   = "5900"
	novncPort = "6080"
	novncWeb  = "/usr/share/novnc"
)

// procFS is where process cmdlines and states are read for liveness
// checks. A variable so tests can point it at a fixture tree.
var procFS = "/proc"

// Door is one open login door, as recorded on disk.
type Door struct {
	VNCPid   int    `json:"vnc_pid"`
	NoVNCPid int    `json:"novnc_pid"`
	Expires  int64  `json:"expires_unix"`
	PassFile string `json:"passfile"`
	URL      string `json:"url"`
	Via      string `json:"via"`
	Created  int64  `json:"created_unix"`
}

func doorDir(state browser.State) string {
	return filepath.Join(state.Dir, "state", "login-door")
}

func doorRecord(state browser.State) string {
	return filepath.Join(doorDir(state), "door.json")
}

// tempPassword makes one VNC password. Exactly 8 characters: classic VNC
// authentication truncates longer passwords silently, so a longer one
// would be a weaker one wearing a disguise. Rejection sampling, because
// the alphabet length is not a power of two.
func tempPassword() (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 8)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}

// novncURL builds the door link and the address its listener binds.
// A Tailscale IPv4 address turns the link into one that works from
// anywhere on the tailnet (the Telegram-remote case), and the listener
// binds exactly that address: tailnet members reach it, nothing public
// does. Anything else, including a tailscale that answers garbage, falls
// back to loopback, which needs the caller's own tunnel.
func novncURL() (url, via, bind string) {
	if path, err := exec.LookPath("tailscale"); err == nil {
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if out, err := exec.CommandContext(ctx, path, "ip", "-4").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if ip := strings.TrimSpace(line); validIPv4(ip) {
					return "http://" + ip + ":" + novncPort + "/vnc.html", "tailscale", ip
				}
			}
		}
	}
	return "http://127.0.0.1:" + novncPort + "/vnc.html", "loopback", "127.0.0.1"
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
	if !procAlive(d.VNCPid, "x11vnc") || !procAlive(d.NoVNCPid, "websockify") {
		return nil
	}
	if _, err := os.Stat(d.PassFile); err != nil {
		return nil
	}
	return &d
}

// closeDoor kills the door's processes and shreds its password file.
// Pids are identity-checked first, so a recycled pid kills nothing
// innocent. The kill targets the whole process group: the recorded pids
// are `timeout` wrappers, and killing a wrapper alone would orphan the
// server it guards. Every step is best-effort: a dead group or missing
// file is the desired end state already.
func closeDoor(d *Door) {
	killGroup(d.VNCPid, "x11vnc")
	killGroup(d.NoVNCPid, "websockify")
	shred(d.PassFile)
}

// killGroup kills a process group whose leader is the recorded door
// process. The group (not the pid) is what guarantees the timeout wrapper
// and the server it guards die together.
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

// startDoor opens a fresh login door: temp password, x11vnc, loopback
// websockify, record on disk. On any failure it undoes the partial start.
// Both servers run under `timeout`, so the door processes die at TTL even
// if no later tool call ever reaps the record.
func startDoor(state browser.State) (*Door, string, error) {
	x11vnc, err := exec.LookPath("x11vnc")
	if err != nil {
		return nil, "", fmt.Errorf("x11vnc is not installed, so no login door can open")
	}
	websockify, err := exec.LookPath("websockify")
	if err != nil {
		return nil, "", fmt.Errorf("websockify is not installed, so no login door can open")
	}
	if _, err := exec.LookPath("timeout"); err != nil {
		return nil, "", fmt.Errorf("timeout is not installed, so no login door can open")
	}
	if info, err := os.Stat(novncWeb); err != nil || !info.IsDir() {
		return nil, "", fmt.Errorf("%s is missing, so noVNC has nothing to serve", novncWeb)
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
	passFile := filepath.Join(dir, "vncpass")
	// Pre-created 0600, so the secret never sits under the umask even
	// briefly. The password travels once in argv to x11vnc's own writer;
	// piping it instead is version-fragile (probed: this x11vnc demands
	// an interactive confirmation), and the exposure is one local exec.
	if err := os.WriteFile(passFile, nil, 0o600); err != nil {
		return nil, "", err
	}
	// ponytail: x11vnc's own writer; a hand-rolled obfuscation would be a
	// second, worse copy of it.
	if out, err := exec.Command(x11vnc, "-storepasswd", pass, passFile).CombinedOutput(); err != nil {
		shred(passFile)
		return nil, "", fmt.Errorf("could not store the door password: %v %s", err, bytes.TrimSpace(out))
	}
	_ = os.Chmod(passFile, 0o600)
	ttl := strconv.FormatInt(int64(doorTTL/time.Second), 10)
	undo := func(cmds ...*exec.Cmd) {
		for _, c := range cmds {
			if c != nil && c.Process != nil {
				_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
				// Reap promptly: without Wait the child lingers as a
				// zombie under this long-lived server. (Doors reaped
				// later from the record have no handle to wait on;
				// those zombies clear when the server exits, bounded
				// by door count.)
				_, _ = c.Process.Wait()
			}
		}
		shred(passFile)
	}
	vncLog := &bytes.Buffer{}
	vnc := exec.Command("timeout", ttl+"s", x11vnc, "-display", ":99", "-localhost",
		"-rfbauth", passFile, "-rfbport", vncPort,
		"-forever", "-shared", "-noxdamage", "-quiet")
	// Own process group per door server, so group-kill in closeDoor and
	// undo takes wrapper plus guarded server and never the caller.
	vnc.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	vnc.Stderr = vncLog
	if err := vnc.Start(); err != nil {
		undo()
		return nil, "", fmt.Errorf("could not start x11vnc: %v", err)
	}
	if !aliveSoon(vnc, "x11vnc", 2*time.Second) {
		undo(vnc)
		return nil, "", fmt.Errorf("x11vnc died at startup (port %s busy, or no X display :99): %s",
			vncPort, bytes.TrimSpace(vncLog.Bytes()))
	}
	novncLog := &bytes.Buffer{}
	link, via, bind := novncURL()
	novnc := exec.Command("timeout", ttl+"s", websockify, "--web="+novncWeb,
		bind+":"+novncPort, "127.0.0.1:"+vncPort)
	novnc.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	novnc.Stderr = novncLog
	if err := novnc.Start(); err != nil {
		undo(vnc)
		return nil, "", fmt.Errorf("could not start websockify: %v", err)
	}
	if !aliveSoon(novnc, "websockify", 2*time.Second) {
		undo(vnc, novnc)
		return nil, "", fmt.Errorf("websockify died at startup (port %s busy?): %s",
			novncPort, bytes.TrimSpace(novncLog.Bytes()))
	}
	now := time.Now()
	d := &Door{
		VNCPid:   vnc.Process.Pid,
		NoVNCPid: novnc.Process.Pid,
		Expires:  now.Add(doorTTL).Unix(),
		PassFile: passFile,
		URL:      link,
		Via:      via,
		Created:  now.Unix(),
	}
	if err := writeRecord(state, d); err != nil {
		undo(vnc, novnc)
		return nil, "", err
	}
	return d, pass, nil
}

// aliveSoon reports whether cmd is still the expected living process
// after a grace period.
func aliveSoon(cmd *exec.Cmd, want string, grace time.Duration) bool {
	time.Sleep(grace)
	if cmd.Process == nil {
		return false
	}
	return procAlive(cmd.Process.Pid, want)
}
