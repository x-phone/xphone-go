package dialog

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/x-phone/xphone-go/testutil"
)

func TestDialogMap_StoreAndLoad(t *testing.T) {
	dm := newDialogMap()
	call := testutil.NewMockCall()

	dm.Store("dialog-abc", call)
	got, ok := dm.Load("dialog-abc")

	assert.True(t, ok)
	assert.Equal(t, call, got)
}

func TestDialogMap_LoadMissingKeyReturnsFalse(t *testing.T) {
	dm := newDialogMap()
	_, ok := dm.Load("does-not-exist")
	assert.False(t, ok)
}

func TestDialogMap_DeleteRemovesEntry(t *testing.T) {
	dm := newDialogMap()
	call := testutil.NewMockCall()

	dm.Store("dialog-abc", call)
	dm.Delete("dialog-abc")

	_, ok := dm.Load("dialog-abc")
	assert.False(t, ok)
}

func TestDialogMap_ConcurrentAccessIsSafe(t *testing.T) {
	dm := newDialogMap()
	done := make(chan struct{})

	for i := 0; i < 100; i++ {
		go func(i int) {
			key := fmt.Sprintf("dialog-%d", i)
			dm.Store(key, testutil.NewMockCall())
			dm.Load(key)
			dm.Delete(key)
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 100; i++ {
		<-done
	}
}
