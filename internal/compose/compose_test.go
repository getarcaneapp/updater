package compose

import "testing"

func TestComposeLabels(t *testing.T) {
	labels := map[string]string{
		ProjectLabelKey:     " app ",
		ServiceLabelKey:     "web",
		WorkingDirLabelKey:  " /srv/app ",
		ConfigFilesLabelKey: " compose.yaml, compose.override.yaml ,, ",
	}
	if got := ProjectLabel(labels); got != "app" {
		t.Fatalf("ProjectLabel() = %q, want app", got)
	}
	if got := ServiceLabel(labels); got != "web" {
		t.Fatalf("ServiceLabel() = %q, want web", got)
	}
	if got := WorkingDirLabel(labels); got != "/srv/app" {
		t.Fatalf("WorkingDirLabel() = %q, want /srv/app", got)
	}
	files := ConfigFilesLabel(labels)
	if len(files) != 2 || files[0] != "compose.yaml" || files[1] != "compose.override.yaml" {
		t.Fatalf("ConfigFilesLabel() = %#v, want two config files", files)
	}
}
