//go:build !linux && !darwin && !windows

package disk

import "errors"

// On other systems Dash doesn't measure disks: the runner reports none.
func usage(string) (free, total uint64, err error) { return 0, 0, errors.ErrUnsupported }

func deviceID(string) (string, error) { return "", errors.ErrUnsupported }
