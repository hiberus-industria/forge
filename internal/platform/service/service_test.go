package service

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	u := renderUnit("/usr/local/bin/forge")
	if !strings.Contains(u, "ExecStart=/usr/local/bin/forge daemon run") || !strings.Contains(u, "WantedBy=default.target") {
		t.Fatal(u)
	}
	p := renderPlist("/opt/forge", "/tmp/d.log")
	for _, want := range []string{"<string>/opt/forge</string>", "<key>KeepAlive</key><true/>", "<string>/tmp/d.log</string>", label} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q", want)
		}
	}
}
