//go:build !darwin && !linux && !windows

package main

import "errors"

func lockDatabase(string) (string, func(), error) {
	return "", nil, errors.New("database restore requires a supported host file lock (macOS or Linux)")
}
