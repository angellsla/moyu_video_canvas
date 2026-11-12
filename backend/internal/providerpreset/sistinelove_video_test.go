package providerpreset

import "testing"

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
