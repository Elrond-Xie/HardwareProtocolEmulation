// File: main.go
// Purpose: Starts the bundled GUI and its private .NET runtime.
// Author: Elrond-Xie
// Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.

package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
)

// main starts the GUI using the runtime packaged beside the launcher.
func main() {
	os.Exit(run_gui(os.Args[1:]))
}

// run_gui prepares the private runtime and launches the managed GUI.
func run_gui(args []string) int {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := filepath.Dir(executable)
	library_dir := filepath.Join(root, "lib")
	if err := os.Setenv("DOTNET_ROOT", library_dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if runtime.GOOS == "linux" {
		library_path := library_dir
		if inherited := os.Getenv("LD_LIBRARY_PATH"); inherited != "" {
			library_path += string(os.PathListSeparator) + inherited
		}
		if err := os.Setenv("LD_LIBRARY_PATH", library_path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	} else {
		path := library_dir
		if inherited := os.Getenv("PATH"); inherited != "" {
			path += string(os.PathListSeparator) + inherited
		}
		if err := os.Setenv("PATH", path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if runtime.GOOS == "linux" {
		if err := configure_font_environment(root); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return launch_dotnet_gui(library_dir, args)
}

// configure_font_environment makes bundled fonts available without system installation.
func configure_font_environment(root string) error {
	user_id := "default"
	if current_user, err := user.Current(); err == nil && current_user.Uid != "" {
		user_id = current_user.Uid
	}
	cache_dir := filepath.Join(os.TempDir(), "hpe-fontconfig-cache-"+user_id)
	if err := os.MkdirAll(cache_dir, 0o755); err != nil {
		return err
	}
	var escaped_root bytes.Buffer
	if err := xml.EscapeText(&escaped_root, []byte(filepath.Join(root, "lib", "fonts"))); err != nil {
		return err
	}
	var escaped_cache bytes.Buffer
	if err := xml.EscapeText(&escaped_cache, []byte(cache_dir)); err != nil {
		return err
	}
	font_config := fmt.Sprintf("<fontconfig><dir>%s</dir><cachedir>%s</cachedir><alias><family>sans-serif</family><prefer><family>DejaVu Sans</family><family>Noto Sans SC</family></prefer></alias><alias><family>monospace</family><prefer><family>DejaVu Sans</family></prefer></alias></fontconfig>\n", escaped_root.String(), escaped_cache.String())
	config_path := filepath.Join(cache_dir, "fonts.conf")
	if err := os.WriteFile(config_path, []byte(font_config), 0o600); err != nil {
		return err
	}
	return os.Setenv("FONTCONFIG_FILE", config_path)
}

// launch_dotnet_gui runs the GUI assembly with the private runtime.
func launch_dotnet_gui(library_dir string, args []string) int {
	dotnet_name := "dotnet"
	if runtime.GOOS == "windows" {
		dotnet_name += ".exe"
	}
	managed_entry := filepath.Join(library_dir, "hpe-gui.dll")
	command_args := append([]string{managed_entry}, args...)
	command := exec.Command(filepath.Join(library_dir, dotnet_name), command_args...)
	configure_gui_process(command)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
