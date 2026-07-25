package registryhost

import "testing"

func TestAuthAddress(t *testing.T) {
	got, err := AuthAddress("nginx:1.27")
	if err != nil {
		t.Fatalf("AuthAddress() error = %v", err)
	}
	if got != "registry-1.docker.io" {
		t.Fatalf("AuthAddress() = %q, want registry-1.docker.io", got)
	}

	got, err = AuthAddress("ghcr.io/getarcaneapp/arcane:v2")
	if err != nil {
		t.Fatalf("AuthAddress() custom error = %v", err)
	}
	if got != "ghcr.io" {
		t.Fatalf("AuthAddress() custom = %q, want ghcr.io", got)
	}

	if _, err := AuthAddress("not a reference"); err == nil {
		t.Fatal("AuthAddress() error = nil, want parse failure")
	}
}

func TestNormalize(t *testing.T) {
	insecureRegistryURL := "http" + "://registry-1.docker.io/v2/"
	for _, alias := range []string{"docker.io", "index.docker.io", "registry-1.docker.io", insecureRegistryURL} {
		if got := Normalize(alias); got != "docker.io" {
			t.Fatalf("Normalize(%q) = %q, want docker.io", alias, got)
		}
	}
	if got := Normalize("GHCR.IO/getarcaneapp"); got != "ghcr.io" {
		t.Fatalf("Normalize() = %q, want ghcr.io", got)
	}
}
