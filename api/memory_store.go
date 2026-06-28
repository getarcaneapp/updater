package api

import (
	"context"
	"slices"
	"sync"

	"go.getarcane.app/updater/types"
)

type memoryPendingStore struct {
	mu      sync.Mutex
	records map[string]types.ImageUpdateRecord
}

// NewMemoryPendingStore returns an in-memory pending update store.
func NewMemoryPendingStore(records ...types.ImageUpdateRecord) PendingStore {
	store := &memoryPendingStore{records: make(map[string]types.ImageUpdateRecord, len(records))}
	for _, record := range records {
		store.records[memoryPendingStoreKeyInternal(record)] = record
	}
	return store
}

func (s *memoryPendingStore) PendingImageUpdates(ctx context.Context) ([]types.ImageUpdateRecord, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := make([]string, 0, len(s.records))
	for key := range s.records {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	out := make([]types.ImageUpdateRecord, 0, len(s.records))
	for _, key := range keys {
		out = append(out, s.records[key])
	}
	return out, nil
}

func (s *memoryPendingStore) ClearImageUpdateRecord(ctx context.Context, record types.ImageUpdateRecord) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.records, memoryPendingStoreKeyInternal(record))
	return nil
}

func memoryPendingStoreKeyInternal(record types.ImageUpdateRecord) string {
	if record.ID != "" {
		return record.ID
	}
	return record.ImageRef()
}
