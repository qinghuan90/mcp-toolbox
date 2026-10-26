//go:build desktop && !darwin && !windows

package main

import "errors"

func desktopStartupState(string) (startupState, error) {
	return startupState{Message: "当前系统尚未接入自动启动设置"}, nil
}
func configureDesktopStartup(string, bool) (startupState, error) {
	return startupState{}, errors.New("当前系统尚未接入自动启动设置")
}
