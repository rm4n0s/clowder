package files

// The names of the directories that the files directory keeps besides the
// files themselves. A backup leaves them out (SPEC D59).
const (
	// TmpDir is the directory of the files that are being written.
	TmpDir = tmpDirName
	// NotesDir is the directory of the notes of staged uploads.
	NotesDir = notesDirName
)

// IsKey reports whether name has the form of a key that Save makes: 64
// lowercase hex characters. It is how a backup tells a stored file from
// anything else in the directory.
func IsKey(name string) bool {
	return validKey(name)
}
