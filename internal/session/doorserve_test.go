package session

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDoorBasicAuth(t *testing.T) {
	h := basicAuth("s3cret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for _, tc := range []struct {
		user, pass string
		set        bool
		want       int
	}{
		{"", "", false, http.StatusUnauthorized},
		{"root", "wrong", true, http.StatusUnauthorized},
		{"anyone", "s3cret", true, http.StatusUnauthorized},
		{"root", "s3cret", true, http.StatusOK},
	} {
		req := httptest.NewRequest("GET", "/", nil)
		if tc.set {
			req.SetBasicAuth(tc.user, tc.pass)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%q/%q: got %d, want %d", tc.user, tc.pass, rec.Code, tc.want)
		}
	}
}

func TestPickPortFallsBack(t *testing.T) {
	// Hold doorPort, unless something on this machine already does: the
	// fallback is what is under test either way.
	if l, err := net.Listen("tcp", "127.0.0.1:"+doorPort); err == nil {
		defer l.Close()
	}
	if got := pickPort("127.0.0.1"); got == doorPort || got == "0" || got == "" {
		t.Fatalf("pickPort with %s held = %q, want another free port", doorPort, got)
	}
}

// TestViewerCallAllowlist guards the door's one security property on the
// input side: a viewer drives the page, never scripts it.
func TestViewerCallAllowlist(t *testing.T) {
	for _, tc := range []struct {
		m    viewerMsg
		want string
	}{
		{viewerMsg{Kind: "mouse", Method: "Input.dispatchMouseEvent"}, "Input.dispatchMouseEvent"},
		{viewerMsg{Kind: "key", Method: "Input.dispatchKeyEvent"}, "Input.dispatchKeyEvent"},
		{viewerMsg{Kind: "text", Text: "pw"}, "Input.insertText"},
		{viewerMsg{Kind: "reload"}, "Page.reload"},
		{viewerMsg{Kind: "mouse", Method: "Runtime.evaluate"}, ""},
		{viewerMsg{Kind: "key", Method: "Network.getCookies"}, ""},
		{viewerMsg{Kind: "eval", Method: "Runtime.evaluate"}, ""},
	} {
		got, _, ok := viewerCall(tc.m)
		if (tc.want == "") == ok || got != tc.want {
			t.Errorf("%+v: got %q ok=%v, want %q", tc.m, got, ok, tc.want)
		}
	}
}

func TestIsHomeURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://www.icloud.com/":             true,
		"https://www.icloud.com":              true,
		"https://www.icloud.com/?lang=en":     true,
		"https://icloud.com/":                 true,
		"https://www.icloud.com/notes/":       false,
		"https://www.icloud.com/reminders/":   false,
		"https://evil.example/www.icloud.com": false,
	} {
		if got := isHomeURL(u); got != want {
			t.Errorf("isHomeURL(%q) = %v, want %v", u, got, want)
		}
	}
}
