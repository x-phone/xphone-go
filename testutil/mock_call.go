package testutil

// MockCall is a minimal mock for use in dialog map tests.
type MockCall struct {
	id string
}

// NewMockCall creates a new MockCall.
func NewMockCall() *MockCall {
	return &MockCall{}
}
