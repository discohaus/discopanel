package hub

import (
	"net/url"
	"testing"
)

func TestParseIndexError(t *testing.T) {
	configureForTest(t, Settings{SupportBase: DefaultSupportBase, IndexBase: "https://index.example", IndexEnabled: true, InstallID: testInstallID})
	index := &url.URL{Scheme: "https", Host: "index.example", Path: "/modrinth/v2/search"}
	body := `{"code":"unavailable","message":"modrinth answered 502","origin":"https://api.modrinth.com/v2/search?query=sodium"}`
	got := ParseIndexError(index, []byte(body))
	if got == nil || got.Code != "unavailable" || got.Message != "modrinth answered 502" || got.Origin != "https://api.modrinth.com/v2/search?query=sodium" {
		t.Fatalf("parsed = %+v", got)
	}
	upper := &url.URL{Scheme: "https", Host: "INDEX.example", Path: "/x"}
	if ParseIndexError(upper, []byte(body)) == nil {
		t.Error("host comparison must ignore case")
	}
	cases := map[string]struct {
		u    *url.URL
		body string
	}{
		"other host":        {&url.URL{Scheme: "https", Host: "api.modrinth.com", Path: "/v2/search"}, body},
		"no origin":         {index, `{"code":"not_found","message":"not found"}`},
		"not json":          {index, `<html>Service unavailable</html>`},
		"origin is index":   {index, `{"code":"x","message":"y","origin":"https://index.example/loop"}`},
		"origin is support": {index, `{"code":"x","message":"y","origin":"` + DefaultSupportBase + `/x"}`},
		"bad scheme":        {index, `{"code":"x","message":"y","origin":"ftp://api.modrinth.com/x"}`},
		"no host":           {index, `{"code":"x","message":"y","origin":"/v2/search"}`},
		"nil url":           {nil, body},
	}
	for name, c := range cases {
		if got := ParseIndexError(c.u, []byte(c.body)); got != nil {
			t.Errorf("%s: parsed %+v, want nil", name, got)
		}
	}
}
