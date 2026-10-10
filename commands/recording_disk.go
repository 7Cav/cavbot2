package commands

import "syscall"

// freeDiskSpace reads the space free to the bot on the filesystem holding
// dir, in bytes.
func freeDiskSpace(dir string) (uint64, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dir, &fs); err != nil {
		return 0, err
	}
	return uint64(fs.Bavail) * uint64(fs.Bsize), nil
}
