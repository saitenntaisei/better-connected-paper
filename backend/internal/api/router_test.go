package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveAllowedOrigins(t *testing.T) {
	cases := map[string]struct {
		env  string
		want []string
	}{
		"empty falls back to localhost": {
			env:  "",
			want: defaultLocalOrigins,
		},
		"single origin": {
			env:  "https://bcp.example.com",
			want: []string{"https://bcp.example.com"},
		},
		"comma-separated with whitespace": {
			env:  " https://a.example , https://b.example ,,",
			want: []string{"https://a.example", "https://b.example"},
		},
		"all-whitespace falls back": {
			env:  " , , ",
			want: defaultLocalOrigins,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := resolveAllowedOrigins(func(string) string { return tc.env })
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// Ensures a preflight from a configured production origin passes CORS.
// Uses t.Setenv so the subsequent NewRouter sees the custom allowlist.
func TestCORSAllowsConfiguredOrigin(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "https://bcp-frontend.vercel.app")
	r := NewRouter(Deps{})

	req := httptest.NewRequest(http.MethodOptions, "/api/graph/build", nil)
	req.Header.Set("Origin", "https://bcp-frontend.vercel.app")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://bcp-frontend.vercel.app" {
		t.Errorf("Access-Control-Allow-Origin = %q, want echo of origin", got)
	}
}

func TestCORSRejectsUnlistedOrigin(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "https://bcp-frontend.vercel.app")
	r := NewRouter(Deps{})

	req := httptest.NewRequest(http.MethodOptions, "/api/graph/build", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for disallowed origin", got)
	}
}
