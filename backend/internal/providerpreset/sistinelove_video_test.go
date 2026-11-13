package providerpreset

import "testing"

func TestSistineloveSeedanceResolutionFromModelName(t *testing.T) {
	for _, tc := range []struct {
		model, want string
	}{
		{"seedance-2.0-gz", "720p"},
		{"tr_seedance-2.0", "720p"},
		{"seedance-2.5-20261080-15s", "720p"},
		{"seedance-2.0-1080s", "720p"},
		{"seedance-2.0-10800", "720p"},
		{"seedance-2.0-480", "480p"},
		{"SD-2.5-720p-15s", "720p"},
		{"seedance2.5-gz-1080", "1080p"},
		{"SEEDANCE-2.0_[1080P]", "1080p"},
		{"seedance-2.0-1440p", "1440p"},
		{"seedance-2.0-2160", "2160p"},
		{"seedance-2.0-2K", "1440p"},
		{"seedance-2.0-4k", "2160p"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			if got := SistineloveSeedanceResolution(tc.model); got != tc.want {
				t.Fatalf("resolution=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestSistineloveSeedanceRouteIsScopedToGateway(t *testing.T) {
	for _, model := range []string{"seedance-2.0-gz", "tr_seedance-2.0", "seedance2.5-gz-1080", "SD-2.5-720p-15s"} {
		for _, base := range []string{SistineloveVideo.GenerationOrigin, SistineloveVideo.GenerationOrigin + "/v1", "https://video.sistinelove.cn:443/v1"} {
			if !IsSistineloveSeedance(base, model) {
				t.Fatal("documented route missing", base, model)
			}
		}
		for _, base := range []string{"https://other.example/v1", "https://video.sistinelove.cn.evil.example", "https://video.sistinelove.cn:444", "http://video.sistinelove.cn", "https://user@video.sistinelove.cn"} {
			if IsSistineloveSeedance(base, model) {
				t.Fatal("unrelated route changed", base)
			}
		}
	}
	if IsSistineloveSeedance(SistineloveVideo.GenerationOrigin, "sora-2") {
		t.Fatal("non Seedance route changed")
	}
}
