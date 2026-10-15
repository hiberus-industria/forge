package ecb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fixture is a trimmed eurofxref-hist-90d.xml (real format: gesmes envelope,
// newest day first).
const fixture = `<?xml version="1.0" encoding="UTF-8"?><gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01" xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref"><gesmes:subject>Reference rates</gesmes:subject><gesmes:Sender><gesmes:name>European Central Bank</gesmes:name></gesmes:Sender><Cube><Cube time="2026-10-15"><Cube currency="USD" rate="1.1208"/><Cube currency="JPY" rate="177.06"/></Cube>
<Cube time="2026-10-14"><Cube currency="JPY" rate="177.39"/><Cube currency="USD" rate="1.1195"/></Cube>
<Cube time="2026-10-09"><Cube currency="USD" rate="1.1206"/></Cube>
</Cube></gesmes:Envelope>`

func newTestSource(t *testing.T, requested *string) *Source {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requested = r.URL.Path
		w.Write([]byte(fixture))
	}))
	t.Cleanup(srv.Close)
	s := NewWithURL(srv.URL, srv.Client())
	s.now = func() time.Time { return time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC) }
	return s
}

func TestFetchRates(t *testing.T) {
	var path string
	s := newTestSource(t, &path)
	rates, err := s.FetchRates(context.Background(), time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if path != "/eurofxref-hist-90d.xml" {
		t.Fatalf("requested %s", path)
	}
	if len(rates) != 2 {
		t.Fatalf("rates = %+v", rates)
	}
	if rates[0].Date != time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC) || rates[0].USDPerEUR != 1.1195 || rates[1].USDPerEUR != 1.1208 {
		t.Fatalf("rates = %+v", rates)
	}
}

func TestFileSelection(t *testing.T) {
	var path string
	s := newTestSource(t, &path)
	now := s.now()
	cases := map[time.Time]string{
		{}:                      "eurofxref-hist.xml",
		now.AddDate(0, -6, 0):   "eurofxref-hist.xml",
		now.AddDate(0, 0, -30):  "eurofxref-hist-90d.xml",
		now.Add(-6 * time.Hour): "eurofxref-daily.xml",
	}
	for since, want := range cases {
		if got := s.file(since); got != want {
			t.Errorf("file(%v) = %s, want %s", since, got, want)
		}
	}
}
