package dialog

import "sync"

// dialogMap is a concurrent-safe map for tracking SIP dialogs.
type dialogMap struct {
	m sync.Map
}

func newDialogMap() *dialogMap {
	return &dialogMap{}
}

// Store adds or updates a dialog entry.
func (dm *dialogMap) Store(key string, value any) {
	dm.m.Store(key, value)
}

// Load retrieves a dialog entry.
func (dm *dialogMap) Load(key string) (any, bool) {
	return dm.m.Load(key)
}

// Delete removes a dialog entry.
func (dm *dialogMap) Delete(key string) {
	dm.m.Delete(key)
}
