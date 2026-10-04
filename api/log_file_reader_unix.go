//go:build !windows

package api

import "os"

func openLogFileReader(path string) (*os.File, error) {
	return os.Open(path)
}
