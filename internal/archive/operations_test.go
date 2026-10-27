package archive

func init() {
	// A programming error in the registry (an invalid transition, an
	// unknown kind) fails the test that makes it.
	strictOperations = true
}
