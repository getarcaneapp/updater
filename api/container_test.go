package api

import (
	"context"
	"testing"

	"go.getarcane.app/updater/types"
)

func TestClearPendingRecordInternal(t *testing.T) {
	latest := "1.28"
	tests := []struct {
		name        string
		appliedRef  string
		record      types.ImageUpdateRecord
		wantCleared bool
	}{
		{
			name:        "digest record cleared by short ref",
			appliedRef:  "nginx:1.27",
			record:      types.ImageUpdateRecord{ID: "digest-rec", Repository: "nginx", Tag: "1.27", HasUpdate: true, UpdateType: types.UpdateTypeDigest},
			wantCleared: true,
		},
		{
			name:        "digest record cleared across registry alias",
			appliedRef:  "docker.io/library/nginx:1.27",
			record:      types.ImageUpdateRecord{ID: "digest-rec", Repository: "nginx", Tag: "1.27", HasUpdate: true, UpdateType: types.UpdateTypeDigest},
			wantCleared: true,
		},
		{
			name:        "tag record kept when only old tag re-pulled",
			appliedRef:  "docker.io/library/nginx:1.27",
			record:      types.ImageUpdateRecord{ID: "tag-rec", Repository: "nginx", Tag: "1.27", HasUpdate: true, UpdateType: types.UpdateTypeTag, LatestVersion: &latest},
			wantCleared: false,
		},
		{
			name:        "tag record cleared when new tag applied",
			appliedRef:  "nginx:1.28",
			record:      types.ImageUpdateRecord{ID: "tag-rec", Repository: "nginx", Tag: "1.27", HasUpdate: true, UpdateType: types.UpdateTypeTag, LatestVersion: &latest},
			wantCleared: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakePendingStore{records: []types.ImageUpdateRecord{tt.record}}
			service := NewService(Config{PendingStore: store})

			service.clearPendingRecordInternal(context.Background(), tt.appliedRef)

			if cleared := len(store.cleared) > 0; cleared != tt.wantCleared {
				t.Fatalf("cleared = %v (%v), want %v", cleared, store.cleared, tt.wantCleared)
			}
		})
	}
}
