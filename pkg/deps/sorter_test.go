package deps

import "testing"

func TestContainerSorterOrdersDependenciesFirstInternal(t *testing.T) {
	sorter := NewContainerSorter([]ContainerWithDeps{
		{Name: "web", DependsOn: []string{"db"}},
		{Name: "db"},
	})

	got, err := sorter.Sort()
	if err != nil {
		t.Fatalf("Sort() error = %v", err)
	}
	if len(got) != 2 || got[0].Name != "db" || got[1].Name != "web" {
		t.Fatalf("Sort() = %#v, want db then web", got)
	}
}

func TestUpdateImplicitRestartInternal(t *testing.T) {
	containers := []ContainerWithDeps{
		{Name: "web", DependsOn: []string{"db"}},
		{Name: "db"},
	}
	marked := map[string]bool{"db": true}

	got := UpdateImplicitRestart(containers, marked)
	if len(got) != 1 || got[0] != "web" {
		t.Fatalf("UpdateImplicitRestart() = %#v, want web", got)
	}
	if !marked["web"] {
		t.Fatal("web was not marked for restart")
	}
}

func TestContainerSorterReportsCyclePathInternal(t *testing.T) {
	sorter := NewContainerSorter([]ContainerWithDeps{
		{Name: "a", DependsOn: []string{"b"}},
		{Name: "b", DependsOn: []string{"a"}},
	})

	_, err := sorter.Sort()
	if err == nil {
		t.Fatal("Sort() error = nil, want circular dependency")
	}
	if got, want := err.Error(), "circular dependency detected: a -> b -> a"; got != want {
		t.Fatalf("Sort() error = %q, want %q", got, want)
	}
}
