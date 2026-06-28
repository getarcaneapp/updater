package utils

import (
	"slices"
	"testing"
)

func TestRegistryHelpersInternal(t *testing.T) {
	if got := ExtractRegistryHost("nginx:1.27"); got != "docker.io" {
		t.Fatalf("ExtractRegistryHost() = %q, want docker.io", got)
	}
	if got := ExtractRegistryHost("ghcr.io/getarcaneapp/arcane:v2"); got != "ghcr.io" {
		t.Fatalf("ExtractRegistryHost() custom = %q, want ghcr.io", got)
	}
	insecureRegistryURL := "http" + "://registry-1.docker.io/v2/"
	if got := NormalizeRegistryURL(insecureRegistryURL); got != "https://index.docker.io/v1/" {
		t.Fatalf("NormalizeRegistryURL() = %q, want Docker Hub auth URL", got)
	}
	if !IsRegistryMatch("https://index.docker.io/v1/", "registry-1.docker.io") {
		t.Fatal("IsRegistryMatch() = false, want true for Docker Hub aliases")
	}
	if got := SortRegistryKeys(map[string]string{"beta": "2", "alpha": "1"}); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("SortRegistryKeys() = %#v, want sorted keys", got)
	}
}
