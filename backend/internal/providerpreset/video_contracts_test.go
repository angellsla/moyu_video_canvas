package providerpreset

import "testing"

func TestEveryDeclaredVideoContractHasKnownProtocolAndValidLimits(t *testing.T) {
	seen := map[string]bool{}
	for _, contract := range videoContracts {
		if len(contract.Models) == 0 || !knownProtocol(contract.Protocol) {
			t.Fatalf("invalid protocol contract: %#v", contract)
		}
		for _, name := range contract.Models {
			if name == "" || seen[name] {
				t.Fatalf("empty or duplicate model: %q", name)
			}
			seen[name] = true
		}
		for kind, count := range contract.MaxReferences {
			if (kind != "image" && kind != "video" && kind != "audio") || count < 0 {
				t.Fatalf("invalid reference limit: %s=%d", kind, count)
			}
		}
	}
}

func TestVideoContractBoundaries(t *testing.T) {
	for _, name := range []string{"seedance-2.0", "seedance-2.5"} {
		contract, ok := VideoContractForModel(name)
		if !ok || !knownProtocol(contract.Protocol) || !contract.InlineMedia {
			t.Fatalf("missing verified contract for %s", name)
		}
	}
	wan, ok := VideoContractForModel("wan3.0-video")
	if !ok || wan.Protocol != "newapi-channel-2" || wan.InlineMedia || !wan.RequiresHTTPReferences {
		t.Fatalf("Wan must use uploaded HTTP references: %#v", wan)
	}
	for _, name := range []string{"wan3.0-video-prime", "wan4.0-video", "seedance-2.50", "future-video"} {
		if _, ok := VideoContractForModel(name); ok {
			t.Fatalf("unverified model matched: %s", name)
		}
	}
}
