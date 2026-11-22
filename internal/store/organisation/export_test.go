package organisation

// SetMaxSelected lowers the cap on objects a filter may select, for a test, and returns the function that restores it.
func SetMaxSelected(n int) (restore func()) {
	old := maxSelected
	maxSelected = n
	return func() { maxSelected = old }
}
