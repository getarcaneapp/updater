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
