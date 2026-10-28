package reconcile

import (
	"os"
	"syscall"
)

func fileUID(fi os.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return 0
}

func fileGID(fi os.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Gid)
	}
	return 0
}
