package generation

import (
	"strings"
	"testing"

	"infinite-canvas/backend/internal/model"
)

func TestDiagnosticProviderStatusRequiresTerminalResponse(t *testing.T) {
	for _, source := range []string{"upstream_response", "upstream_http", "local_result"} {
		for _, status := range []string{"", "failed", "cancelled", "unknown", "processing"} {
			d := SanitizeTaskDiagnostics(&model.TaskFailureDiagnostics{Source: source, ProviderStatus: status})
			want := ""
			if source == "upstream_response" && (status == "failed" || status == "cancelled") {
				want = status
			}
			if d.ProviderStatus != want {
				t.Fatalf("source=%q status=%q: got %q, want %q", source, status, d.ProviderStatus, want)
			}
		}
	}
}

func TestDiagnosticSummaryHidesMountedAndNetworkPaths(t *testing.T) {
	for _, path := range []string{`/Volumes/PrivateDrive/customer-A/image.png`, `/Volumes/External Work/customer-A/image.png`, `C:\Private User\customer-A\image.png`, `\\Private Server\Private Share\customer-A\image.png`, `/mnt/PrivateDrive/customer-A/image.png`, `/media/PrivateDrive/customer-A/image.png`, `\\PrivateServer\PrivateShare\customer-A\image.png`} {
		got := DiagnosticSummary("open " + path + ": permission denied")
		if strings.Contains(got, "Private") || strings.Contains(got, "customer-A") || !strings.Contains(got, "permission denied") {
			t.Fatalf("unsafe summary %q", got)
		}
	}
}
