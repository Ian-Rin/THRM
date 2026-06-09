//go:build !windows && !linux

package temperature

func (r *Reader) readPlatformCPUTemp() int {
	return 0
}

