package updater

import (
	"context"
	"slices"
	"sync"
)

type memoryPendingStore struct {
	mu      sync.Mutex
	records map[string]ImageUpdateRecord
}

// NewMemoryPendingStore returns an in-memory pending update store.
func NewMemoryPendingStore(records ...ImageUpdateRecord) PendingStore {
	store := &memoryPendingStore{records: make(map[string]ImageUpdateRecord, len(records))}
	for _, record := range records {
		store.records[memoryPendingStoreKey(record)] = record
	}
	return store
}

func (s *memoryPendingStore) PendingImageUpdates(ctx context.Context) ([]ImageUpdateRecord, error) {
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

	out := make([]ImageUpdateRecord, 0, len(s.records))
	for _, key := range keys {
		out = append(out, s.records[key])
	}
	return out, nil
}

func (s *memoryPendingStore) ClearImageUpdateRecord(ctx context.Context, record ImageUpdateRecord) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.records, memoryPendingStoreKey(record))
	return nil
}

func memoryPendingStoreKey(record ImageUpdateRecord) string {
	if record.ID != "" {
		return record.ID
	}
	return record.ImageRef()
}
