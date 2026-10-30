package skillhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientRejectsUntrustedArchiveAndEncodedTraversal(t *testing.T) {
	c, err := New(DefaultURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"https://evil.invalid/archive", "//evil.invalid/archive", "/api/beeftv/skillhub/v1/%2e%2e/secret", "https://beeftv.app/other-api", "../private"} {
		if _, err = c.URL(route); err == nil {
			t.Fatal("untrusted archive", route)
		}
	}
}

func TestArchiveFetchesOnlyFromConfiguredMarket(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte("archive"))
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	release := Release{Metadata: Metadata{SkillID: "one", Name: "One"}, ReleaseID: "r1", DownloadURL: server.URL + "/archive"}
	if _, err = client.Archive(context.Background(), &release); err != nil || requests != 1 {
		t.Fatalf("archive was not fetched once: requests=%d err=%v", requests, err)
	}
	release.DownloadURL = "https://example.invalid/archive"
	if _, err = client.Archive(context.Background(), &release); err == nil || requests != 1 {
		t.Fatalf("cross-origin archive was accepted: requests=%d err=%v", requests, err)
	}
}
