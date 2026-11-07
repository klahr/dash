package orgdomain

import (
	"slices"
	"testing"
)

func TestNewChecksTheDomain(t *testing.T) {
	for _, bad := range []string{"", "localhost", "https://dash.example.com", "dash.example.com:443", "10.0.0.1", "-x.example.com", "dash..example.com", "dash_x.example.com"} {
		if _, err := New(bad, nil, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	d, err := New(" Dash.Example.com. ", nil, "")
	if err != nil || d.Base != "dash.example.com" {
		t.Fatalf("%v %+v", err, d)
	}
	if _, err := New("dash.example.com", nil, "dash.example.com"); err == nil {
		t.Error("preview domain == org domain accepted")
	}
	if _, err := New("x.preview.example.com", nil, "preview.example.com"); err == nil {
		t.Error("org domain under the preview domain accepted")
	}
}

func TestReserved(t *testing.T) {
	d, _ := New("dash.example.com", nil, "preview.dash.example.com")
	for _, r := range []string{"www", "api", "admin", "preview", "billing", "WWW"} {
		if !d.Reserved(r) || d.Usable(r) {
			t.Errorf("%q not reserved", r)
		}
	}
	if d.Reserved("acme") || !d.Usable("acme") {
		t.Error("acme reserved")
	}
	// The preview domain's labels are reserved whatever the list.
	d, _ = New("dash.example.com", []string{"foo"}, "pv.apps.dash.example.com")
	for _, r := range []string{"foo", "pv", "apps"} {
		if !d.Reserved(r) {
			t.Errorf("%q not reserved", r)
		}
	}
	if d.Reserved("www") {
		t.Error("a configured list should replace the defaults")
	}
	d, _ = New("dash.example.com", nil, "previews.example.net")
	if !d.Reserved("previews") {
		t.Error("the preview domain's first label isn't reserved")
	}
	if got := ParseList(" a, b c,,"); !slices.Equal(got, []string{"a", "b", "c"}) || ParseList(" ") != nil {
		t.Errorf("ParseList: %q", got)
	}
}

func TestParse(t *testing.T) {
	d, _ := New("dash.example.com", nil, "preview.dash.example.com")
	for _, tc := range []struct {
		host string
		kind Kind
		slug string
	}{
		{"dash.example.com", Base, ""},
		{"DASH.example.com.:8443", Base, ""},
		{"acme.dash.example.com", Org, "acme"},
		{"Acme.Dash.Example.Com:443", Org, "acme"},
		{"www.dash.example.com", Invalid, "www"},
		{"preview.dash.example.com", Invalid, "preview"},
		{"t1.preview.dash.example.com", Invalid, "t1.preview"},
		{"ab.dash.example.com", Invalid, "ab"},
		{"xn--bcher-kva.dash.example.com", Invalid, "xn--bcher-kva"},
		{"localhost:7788", Elsewhere, ""},
		{"127.0.0.1", Elsewhere, ""},
		{"[::1]:7788", Elsewhere, ""},
		{"evildash.example.com", Elsewhere, ""},
		{"example.com", Elsewhere, ""},
	} {
		k, s := d.Parse(tc.host)
		if k != tc.kind || s != tc.slug {
			t.Errorf("%s: %v %q, want %v %q", tc.host, k, s, tc.kind, tc.slug)
		}
	}
}

func TestURLs(t *testing.T) {
	d, _ := New("dash.example.com", nil, "")
	if got := d.URL("https", "acme", ""); got != "https://acme.dash.example.com" {
		t.Error(got)
	}
	if got := d.URL("http", "", "7788"); got != "http://dash.example.com:7788" {
		t.Error(got)
	}
	if d.Scheme() != "http" || d.Origin("acme") != "https://acme.dash.example.com" {
		t.Errorf("defaults: %s %s", d.Scheme(), d.Origin("acme"))
	}
	d.UseDefaults("https://dash.example.com:8443")
	if d.Scheme() != "https" || d.Origin("acme") != "https://acme.dash.example.com:8443" {
		t.Errorf("from the public URL: %s %s", d.Scheme(), d.Origin("acme"))
	}
	d2, _ := New("dash.example.com", nil, "")
	d2.UseDefaults("https://other.example.com")
	if d2.Scheme() != "http" {
		t.Error("a public URL elsewhere was used")
	}
}
