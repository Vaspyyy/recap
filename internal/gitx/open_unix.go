//go:build unix

package gitx

import "syscall"

// openFlags open a working-tree file without following a symlink and
// without blocking on a named pipe that took the file's place since it
// was checked.
const openFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
