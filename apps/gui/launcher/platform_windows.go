//go:build windows

// File: platform_windows.go
// Purpose: Configures the project GUI process on Windows hosts.
// Author: Elrond-Xie
// Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.

package main

import (
	"os/exec"
	"syscall"
)

// configure_gui_process prevents the .NET host from opening a console window.
func configure_gui_process(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
