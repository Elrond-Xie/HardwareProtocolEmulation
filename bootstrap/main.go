// File: main.go
// Purpose: Self-contained cross-platform project bootstrap, build, and cleanup tool.
// Author: Elrond-Xie
// Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

const (
	zig_version    = "0.17.0"
	cmake_version  = "4.4.4"
	ninja_version  = "1.13.1"
	python_version = "3.13.9"
	dotnet_version = "10.0.100"
	max_build_logs = 10
)

type layout struct {
	root, build, tools, downloads, toolchain string
}

type artifact struct {
	name, url, sha256, destination, format string
	strip                                  int
}

// pinned_source describes a source revision and its platform-specific archive fallbacks.
type pinned_source struct {
	repository, revision        string
	linux_url, linux_sha256     string
	windows_url, windows_sha256 string
}

// main implements the main operation.
func main() {
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(os.Args[0]), filepath.Ext(os.Args[0])))
	if name == "hpe-zig-cc" || name == "hpe-zig-cxx" {
		os.Exit(run_zig_wrapper(name == "hpe-zig-cxx", os.Args[1:]))
	}
	if name == "git" {
		os.Exit(run_git_version_shim(os.Args[1:]))
	}
	if name == "hpe-gui" && runtime.GOOS == "linux" {
		os.Exit(run_linux_gui_launcher(os.Args[1:]))
	}
	if strings.HasPrefix(name, "hpe-zig-") {
		os.Exit(run_zig_tool(strings.TrimPrefix(name, "hpe-zig-"), os.Args[1:]))
	}
	os.Exit(run_bootstrap(os.Args[1:]))
}

// run_bootstrap implements the run bootstrap operation.
func run_bootstrap(args []string) int {
	var capture *build_log
	command_name := ""
	if len(args) > 0 {
		switch args[0] {
		case "tools", "build", "clean", "distclean":
			command_name = args[0]
		}
	}
	if command_name != "" {
		paths, err := project_layout()
		if err != nil {
			fmt.Fprintln(os.Stderr, "hpe-bootstrap:", err)
			return 1
		}
		if err := prepare_managed_roots(paths); err != nil {
			fmt.Fprintln(os.Stderr, "hpe-bootstrap:", err)
			return 1
		}
		log_dir := build_log_directory(paths)
		capture, err = start_build_log(log_dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "hpe-bootstrap: cannot create build log:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "Build log: %s\n", capture.path)
		fmt.Fprintf(os.Stdout, "HPE %s started at %s (%s/%s)\n", command_name, time.Now().Format(time.RFC3339), runtime.GOOS, runtime.GOARCH)
		fmt.Fprintf(os.Stdout, "Working directory: %q\nProject root: %q\nBuild root: %q\nToolchain root: %q\nCommand: %s\n\n",
			current_directory(), paths.root, paths.build, paths.toolchain, format_command("hpe-bootstrap", args))
		if err := prune_build_logs(filepath.Dir(capture.path), max_build_logs); err != nil {
			fmt.Fprintf(os.Stderr, "Build log retention failed: %v\n", err)
			_ = capture.Close()
			return 1
		}
	}

	err := run(args)
	code := 0
	if err != nil {
		fmt.Fprintln(os.Stderr, "hpe-bootstrap:", err)
		code = 1
	}
	if capture != nil {
		status := "succeeded"
		if code != 0 {
			status = "failed"
		}
		fmt.Fprintf(os.Stdout, "\nHPE %s %s at %s\nBuild log saved to: %s\n", command_name, status, time.Now().Format(time.RFC3339), capture.path)
		if close_err := capture.Close(); close_err != nil {
			fmt.Fprintf(capture.console_err, "hpe-bootstrap: finalize build log: %v\n", close_err)
			code = 1
		}
	}
	return code
}

// prepare_managed_roots implements the prepare managed roots operation.
func prepare_managed_roots(paths layout) error {
	root_build := filepath.Join(paths.root, "build")
	default_toolchain := filepath.Join(paths.build, "toolchains")
	root_install := filepath.Join(paths.root, "install")
	default_install := filepath.Join(root_install, host_runtime_id())
	custom_install := env_path("HPE_INSTALL_ROOT", default_install)
	managed_roots := []string{paths.build, root_install}
	toolchain_inside_build := same_path(paths.build, paths.toolchain) || path_contains(paths.build, paths.toolchain)
	if toolchain_inside_build && !same_path(default_toolchain, paths.toolchain) {
		return fmt.Errorf("HPE_TOOL_ROOT must be outside HPE_BUILD_ROOT or equal %s", default_toolchain)
	}
	if !toolchain_inside_build {
		managed_roots = append(managed_roots, paths.toolchain)
	}
	if !same_path(custom_install, root_install) && !path_contains(root_install, custom_install) {
		managed_roots = append(managed_roots, custom_install)
	}
	if err := validate_disjoint_generated_paths(managed_roots...); err != nil {
		return err
	}
	build_entries := []string{"logs", "downloads", "tools", "toolchains", "bootstrap-toolchain", "bootstrap-go-cache", "bootstrap-mod-cache", "projects", "ccache-build", "ccache-source", "ccache-cache", "vcpkg-root", "vcpkg-downloads", "host-tools", "manifests", "nuget-packages", "nuget-http-cache"}
	if log_entry, in_build_root, err := build_log_root_entry(paths); err != nil {
		return err
	} else if in_build_root && log_entry != "." {
		build_entries = append(build_entries, log_entry)
	}
	if err := prepare_managed_root(paths.root, paths.build, root_build, ".hpe-build-root", build_entries); err != nil {
		return err
	}
	if !toolchain_inside_build {
		if err := prepare_managed_root(paths.root, paths.toolchain, default_toolchain, ".hpe-toolchain-root", []string{"zig", "dotnet"}); err != nil {
			return err
		}
	}
	if same_path(custom_install, default_install) || same_path(custom_install, root_install) || path_contains(root_install, custom_install) {
		return nil
	}
	return prepare_managed_root(paths.root, custom_install, default_install, ".hpe-install-root",
		[]string{"gui", "lib", "examples", "hpe-cli", "hpe-cli.exe", "hpe-gui", "hpe-gui.cmd", "hpe_core.dll", "hpe_gui_bridge.dll"})
}

// build_log_directory implements the build log directory operation.
func build_log_directory(paths layout) string {
	return env_path("HPE_LOG_DIR", filepath.Join(paths.build, "logs"))
}

// build_log_root_entry implements the build log root entry operation.
func build_log_root_entry(paths layout) (string, bool, error) {
	log_dir, err := filepath.Abs(build_log_directory(paths))
	if err != nil {
		return "", false, err
	}
	build_root, err := filepath.Abs(paths.build)
	if err != nil {
		return "", false, err
	}
	relative, err := filepath.Rel(build_root, log_dir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", false, nil
	}
	if relative == "." {
		return ".", true, nil
	}
	return strings.Split(relative, string(os.PathSeparator))[0], true, nil
}

// prepare_managed_root implements the prepare managed root operation.
func prepare_managed_root(project_root, directory, default_directory, marker_name string, allowed_entries []string) error {
	path, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to manage symlink project root %s", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if same_path(path, default_directory) {
		return nil
	}
	if err := validate_generated_path(project_root, path); err != nil {
		return fmt.Errorf("unsafe custom project root: %w", err)
	}
	marker := filepath.Join(path, marker_name)
	expected := filepath.Clean(path) + "\nproject=" + filepath.Clean(project_root) + "\n"
	if data, err := os.ReadFile(marker); err == nil {
		if string(data) != expected {
			return fmt.Errorf("custom project root marker does not match %s", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
		entries = nil
	} else if err != nil {
		return err
	}
	allowed := make(map[string]bool, len(allowed_entries))
	for _, entry := range allowed_entries {
		allowed[entry] = true
	}
	for _, entry := range entries {
		if marker_name == ".hpe-build-root" && strings.HasPrefix(entry.Name(), "build-") && strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		if !allowed[entry.Name()] {
			return fmt.Errorf("custom project root %s contains unrecognized entry %q; choose a dedicated empty HPE output directory", path, entry.Name())
		}
	}
	if err := os.WriteFile(marker, []byte(expected), 0o644); err != nil {
		return fmt.Errorf("mark project-owned directory %s: %w", path, err)
	}
	return nil
}

// prune_build_logs implements the prune build logs operation.
func prune_build_logs(directory string, keep int) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	type log_entry struct {
		name     string
		mod_time time.Time
	}
	logs := make([]log_entry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "build-") || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %s: %w", entry.Name(), err)
		}
		if info.Mode().IsRegular() {
			logs = append(logs, log_entry{name: entry.Name(), mod_time: info.ModTime()})
		}
	}
	sort.Slice(logs, func(i, j int) bool {
		if logs[i].mod_time.Equal(logs[j].mod_time) {
			return logs[i].name < logs[j].name
		}
		return logs[i].mod_time.Before(logs[j].mod_time)
	})
	for len(logs) > keep {
		oldest := logs[0]
		fmt.Printf("Removing oldest build log to retain the latest %d: %s\n", keep, oldest.name)
		if err := os.Remove(filepath.Join(directory, oldest.name)); err != nil {
			return fmt.Errorf("remove old build log %s: %w", oldest.name, err)
		}
		logs = logs[1:]
	}
	return nil
}

type build_log struct {
	path        string
	file        *os.File
	console_out *os.File
	console_err *os.File
	stdout_pipe *os.File
	stderr_pipe *os.File
	drains      sync.WaitGroup
}

type synchronized_writer struct {
	mu sync.Mutex
	w  io.Writer
}

// Write implements the Write operation.
func (w *synchronized_writer) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(data)
}

// start_build_log implements the start build log operation.
func start_build_log(directory string) (*build_log, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(directory, "build-"+time.Now().Format("20060102-150405")+"-*.log")
	if err != nil {
		return nil, err
	}
	stdout_read, stdout_pipe, err := os.Pipe()
	if err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	stderr_read, stderr_pipe, err := os.Pipe()
	if err != nil {
		stdout_read.Close()
		stdout_pipe.Close()
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	log := &build_log{
		path: filepath.Clean(file.Name()), file: file,
		console_out: os.Stdout, console_err: os.Stderr,
		stdout_pipe: stdout_pipe, stderr_pipe: stderr_pipe,
	}
	shared_log := &synchronized_writer{w: file}
	log.drains.Add(2)
	go func() {
		defer log.drains.Done()
		_, _ = io.Copy(io.MultiWriter(log.console_out, shared_log), stdout_read)
		_ = stdout_read.Close()
	}()
	go func() {
		defer log.drains.Done()
		_, _ = io.Copy(io.MultiWriter(log.console_err, shared_log), stderr_read)
		_ = stderr_read.Close()
	}()
	os.Stdout, os.Stderr = stdout_pipe, stderr_pipe
	return log, nil
}

// Close implements the Close operation.
func (log *build_log) Close() error {
	_ = log.stdout_pipe.Close()
	_ = log.stderr_pipe.Close()
	log.drains.Wait()
	os.Stdout, os.Stderr = log.console_out, log.console_err
	sync_err := log.file.Sync()
	close_err := log.file.Close()
	if sync_err != nil {
		return sync_err
	}
	return close_err
}

// current_directory implements the current directory operation.
func current_directory() string {
	directory, err := os.Getwd()
	if err != nil {
		return "unknown"
	}
	return directory
}

// format_command implements the format command operation.
func format_command(name string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, strconv.Quote(name))
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

// run implements the run operation.
func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Println("Usage: hpe-bootstrap <tools|build|clean|distclean|help>")
		fmt.Println("  tools   Download and install pinned local compiler/build SDKs")
		fmt.Println("  build   Build and install HPE for the current host")
		fmt.Println("  clean   Remove intermediate outputs and installed programs, keeping downloads and caches")
		fmt.Println("  distclean Remove all downloaded and generated build data, keeping the last 10 logs")
		return nil
	}
	if args[0] != "tools" && args[0] != "build" && args[0] != "clean" && args[0] != "distclean" {
		return fmt.Errorf("unknown command %q (run hpe-bootstrap help)", args[0])
	}
	paths, err := project_layout()
	if err != nil {
		return err
	}
	switch args[0] {
	case "build":
		return build_project(paths)
	case "clean":
		return clean_project(paths)
	case "distclean":
		return distclean_project(paths)
	default:
		return run_step("Prepare compiler SDKs and local build tools", func() error { return ensure_tools(paths) })
	}
}

// run_step implements the run step operation.
func run_step(name string, action func() error) error {
	started := time.Now()
	fmt.Printf("\n=== %s ===\n", name)
	if err := action(); err != nil {
		fmt.Printf("=== FAILED: %s (%s): %v ===\n", name, time.Since(started).Round(time.Millisecond), err)
		return fmt.Errorf("%s: %w", name, err)
	}
	fmt.Printf("=== DONE: %s (%s) ===\n", name, time.Since(started).Round(time.Millisecond))
	return nil
}

// clean_project implements the clean project operation.
func clean_project(paths layout) error {
	return run_step("Clean intermediate build outputs and install packages", func() error {
		root_install := filepath.Join(paths.root, "install")
		custom_install := env_path("HPE_INSTALL_ROOT", root_install)
		remove := []string{
			filepath.Join(paths.build, "projects"),
			filepath.Join(paths.build, "ccache-build"),
			filepath.Join(paths.build, "host-tools", "build"),
			filepath.Join(paths.build, "manifests"),
			root_install,
		}
		root_dotnet_build := filepath.Join(paths.root, "build", "dotnet")
		if !same_path(root_dotnet_build, paths.build) && !path_contains(root_dotnet_build, paths.build) && !path_contains(paths.build, root_dotnet_build) {
			remove = append(remove, root_dotnet_build)
		}
		if !same_path(custom_install, root_install) && !path_contains(root_install, custom_install) {
			remove = append(remove, custom_install)
		}
		protected := []string{paths.build, root_install}
		if !same_path(paths.build, paths.toolchain) && !path_contains(paths.build, paths.toolchain) {
			protected = append(protected, paths.toolchain)
		}
		if !same_path(custom_install, root_install) && !path_contains(root_install, custom_install) {
			protected = append(protected, custom_install)
		}
		if !same_path(root_dotnet_build, paths.build) && !path_contains(root_dotnet_build, paths.build) && !path_contains(paths.build, root_dotnet_build) {
			protected = append(protected, root_dotnet_build)
		}
		if err := validate_disjoint_generated_paths(protected...); err != nil {
			return err
		}
		for _, target := range remove {
			if err := validate_generated_path(paths.root, target); err != nil {
				return err
			}
		}
		for _, target := range remove {
			if err := remove_generated_path(paths.root, target); err != nil {
				return err
			}
		}
		fmt.Printf("Kept downloaded archives, compiler SDKs, dependency sources, installed build tools, ccache, package caches, and build logs.\n")
		return nil
	})
}

// distclean_project implements the distclean project operation.
func distclean_project(paths layout) error {
	return run_step("Remove downloaded toolchains, dependencies, intermediate outputs, and installed programs", func() error {
		root_install := filepath.Join(paths.root, "install")
		custom_install := env_path("HPE_INSTALL_ROOT", root_install)
		remove := []string{root_install}
		if !same_path(paths.build, paths.toolchain) && !path_contains(paths.build, paths.toolchain) {
			remove = append(remove, paths.toolchain)
		}
		if !same_path(custom_install, root_install) && !path_contains(root_install, custom_install) {
			remove = append(remove, custom_install)
		}
		protected := []string{paths.build, root_install}
		if !same_path(paths.build, paths.toolchain) && !path_contains(paths.build, paths.toolchain) {
			protected = append(protected, paths.toolchain)
		}
		if !same_path(custom_install, root_install) && !path_contains(root_install, custom_install) {
			protected = append(protected, custom_install)
		}
		root_dotnet_build := filepath.Join(paths.root, "build", "dotnet")
		if !same_path(root_dotnet_build, paths.build) && !path_contains(root_dotnet_build, paths.build) && !path_contains(paths.build, root_dotnet_build) {
			protected = append(protected, root_dotnet_build)
		}
		if err := validate_disjoint_generated_paths(protected...); err != nil {
			return err
		}
		if err := validate_generated_path(paths.root, paths.build); err != nil {
			return err
		}
		for _, target := range remove {
			if err := validate_generated_path(paths.root, target); err != nil {
				return err
			}
		}
		root_dotnet_build = filepath.Join(paths.root, "build", "dotnet")
		if !same_path(root_dotnet_build, paths.build) && !path_contains(root_dotnet_build, paths.build) {
			if err := remove_generated_path(paths.root, root_dotnet_build); err != nil {
				return err
			}
		}
		for _, target := range []string{filepath.Join(paths.root, "out"), filepath.Join(paths.root, ".ccache")} {
			managed := false
			for _, root := range append(append([]string(nil), protected...), custom_install) {
				if paths_overlap(target, root) {
					managed = true
					break
				}
			}
			if !managed {
				if err := remove_generated_path(paths.root, target); err != nil {
					return err
				}
			}
		}
		for _, target := range remove {
			if err := remove_generated_path(paths.root, target); err != nil {
				return err
			}
		}
		build_info, err := os.Stat(paths.build)
		if errors.Is(err, os.ErrNotExist) {
			fmt.Printf("Build root already absent: %s\n", paths.build)
			return nil
		}
		if err != nil {
			return err
		}
		if !build_info.IsDir() {
			return fmt.Errorf("build root is not a directory: %s", paths.build)
		}
		if err := validate_generated_path(paths.root, paths.build); err != nil {
			return err
		}
		entries, err := os.ReadDir(paths.build)
		if err != nil {
			return err
		}
		log_entry, log_dir_in_build_root, err := build_log_root_entry(paths)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if (entry.Name() == "logs" && entry.IsDir()) || (log_dir_in_build_root && log_entry != "." && entry.Name() == log_entry && entry.IsDir()) {
				fmt.Printf("Keeping bounded build log history: %s\n", filepath.Join(paths.build, entry.Name()))
				continue
			}
			if log_dir_in_build_root && log_entry == "." && !entry.IsDir() && strings.HasPrefix(entry.Name(), "build-") && strings.HasSuffix(entry.Name(), ".log") {
				fmt.Printf("Keeping bounded build log: %s\n", filepath.Join(paths.build, entry.Name()))
				continue
			}
			if entry.Name() == ".hpe-build-root" {
				fmt.Printf("Keeping custom build-root ownership marker: %s\n", filepath.Join(paths.build, entry.Name()))
				continue
			}
			if err := remove_generated_path(paths.root, filepath.Join(paths.build, entry.Name())); err != nil {
				return err
			}
		}
		fmt.Printf("Distclean complete; all tool downloads and generated build data were removed.\n")
		return nil
	})
}

// remove_generated_path implements the remove generated path operation.
func remove_generated_path(project_root, target string) error {
	if err := validate_generated_path(project_root, target); err != nil {
		return err
	}
	if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		fmt.Printf("Already clean: %s\n", target)
		return nil
	} else if err != nil {
		return err
	}
	fmt.Printf("Removing: %s\n", target)
	return os.RemoveAll(target)
}

// validate_generated_path implements the validate generated path operation.
func validate_generated_path(project_root, target string) error {
	root, err := filepath.Abs(project_root)
	if err != nil {
		return err
	}
	path, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	volume_root := filepath.VolumeName(path) + string(os.PathSeparator)
	if same_path(path, volume_root) || same_path(path, root) || path_contains(path, root) {
		return fmt.Errorf("refusing to remove unsafe generated path %s", path)
	}
	return nil
}

// same_path implements the same path operation.
func same_path(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(first, second)
	}
	return first == second
}

// path_contains implements the path contains operation.
func path_contains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

// validate_disjoint_generated_paths implements the validate disjoint generated paths operation.
func validate_disjoint_generated_paths(paths ...string) error {
	for first := 0; first < len(paths); first++ {
		left, err := filepath.Abs(paths[first])
		if err != nil {
			return err
		}
		for second := first + 1; second < len(paths); second++ {
			right, err := filepath.Abs(paths[second])
			if err != nil {
				return err
			}
			if paths_overlap(left, right) {
				return fmt.Errorf("generated paths overlap and cannot be safely cleaned: %s and %s", left, right)
			}
		}
	}
	return nil
}

// paths_overlap implements the paths overlap operation.
func paths_overlap(first, second string) bool {
	return same_path(first, second) || path_contains(first, second) || path_contains(second, first)
}

// ensure_tools implements the ensure tools operation.
func ensure_tools(paths layout) error {
	if err := os.MkdirAll(paths.downloads, 0o755); err != nil {
		return err
	}
	var artifacts []artifact
	if err := run_step("Resolve pinned SDK and host tool checksums", func() error {
		var err error
		artifacts, err = pinned_artifacts(paths)
		return err
	}); err != nil {
		return err
	}
	for _, item := range artifacts {
		if item.destination == "" {
			continue
		}
		if err := run_step("Verify/download/install "+item.name, func() error { return install_artifact(item, paths.downloads) }); err != nil {
			return fmt.Errorf("install %s: %w", item.name, err)
		}
	}
	if runtime.GOOS == "linux" {
		if err := run_step("Prepare project-local compiler and Git compatibility wrappers", func() error { return install_zig_wrappers(paths) }); err != nil {
			return err
		}
		if err := run_step("Build project-local Linux host tools", func() error { return ensure_linux_host_tools(paths, build_jobs()) }); err != nil {
			return err
		}
	}
	fmt.Printf("Local compiler and build tools are ready under %s and %s\n", paths.toolchain, paths.tools)
	return nil
}

// run_zig_wrapper implements the run zig wrapper operation.
func run_zig_wrapper(cxx bool, args []string) int {
	zig := os.Getenv("HPE_ZIG")
	if zig == "" {
		fmt.Fprintln(os.Stderr, "HPE_ZIG is not set")
		return 2
	}
	for _, arg := range args {
		if arg == "-E" {
			for _, input := range args {
				if strings.HasSuffix(input, ".gperf.h") {
					return run_gperf_preprocessor(zig, args)
				}
			}
		}
	}
	compiler := "cc"
	if cxx {
		compiler = "c++"
	}
	command := exec.Command(zig, append([]string{compiler}, args...)...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

// run_zig_tool implements the run zig tool operation.
func run_zig_tool(tool string, args []string) int {
	if tool == "nm" {
		return run_local_nm(args)
	}
	zig := os.Getenv("HPE_ZIG")
	if zig == "" {
		fmt.Fprintln(os.Stderr, "HPE_ZIG is not set")
		return 2
	}
	if tool == "ld" {
		tool = "ld.lld"
	}
	if tool == "ar" {
		var err error
		args, err = expand_at_files(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	command := exec.Command(zig, append([]string{tool}, args...)...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

// run_git_version_shim implements the run git version shim operation.
func run_git_version_shim(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("git version 2.50.1")
		return 0
	}
	for index, arg := range args {
		if arg != "apply" {
			continue
		}
		var patch_path string
		for _, candidate := range args[index+1:] {
			if !strings.HasPrefix(candidate, "-") {
				patch_path = candidate
				break
			}
		}
		if patch_path == "" {
			fmt.Fprintln(os.Stderr, "hpe git compatibility: git apply requires a patch file")
			return 2
		}
		if err := apply_unified_patch(patch_path); err != nil {
			fmt.Fprintln(os.Stderr, "hpe git compatibility:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "hpe bootstrap Git compatibility supports only --version and apply")
	return 1
}

var unified_hunk_header = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

type unified_hunk struct {
	old_start int
	new_start int
	lines     []string
}

// apply_unified_patch implements the apply unified patch operation.
func apply_unified_patch(patch_path string) error {
	patch_bytes, err := os.ReadFile(patch_path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(patch_bytes), "\r\n", "\n"), "\n")
	for index := 0; index < len(lines); {
		if !strings.HasPrefix(lines[index], "--- ") {
			index++
			continue
		}
		old_path := strings.Fields(strings.TrimPrefix(lines[index], "--- "))[0]
		index++
		if index >= len(lines) || !strings.HasPrefix(lines[index], "+++ ") {
			return errors.New("malformed unified patch: missing +++ file header")
		}
		new_path := strings.Fields(strings.TrimPrefix(lines[index], "+++ "))[0]
		index++
		path := new_path
		if path == "/dev/null" {
			path = old_path
		}
		for _, prefix := range []string{"a/", "b/", "i/", "w/"} {
			path = strings.TrimPrefix(path, prefix)
		}
		target := filepath.Clean(filepath.Join(".", filepath.FromSlash(path)))
		if target == "." || target == ".." || strings.HasPrefix(target, ".."+string(filepath.Separator)) || filepath.IsAbs(target) {
			return fmt.Errorf("patch path escapes the working directory: %s", path)
		}
		var hunks []unified_hunk
		for index < len(lines) {
			match := unified_hunk_header.FindStringSubmatch(lines[index])
			if match == nil {
				if strings.HasPrefix(lines[index], "--- ") || strings.HasPrefix(lines[index], "diff --git ") {
					break
				}
				index++
				continue
			}
			old_start, _ := strconv.Atoi(match[1])
			new_start, _ := strconv.Atoi(match[3])
			hunk := unified_hunk{old_start: old_start, new_start: new_start}
			index++
			for index < len(lines) {
				line := lines[index]
				trimmed := strings.TrimLeft(line, " \t")
				if strings.HasPrefix(trimmed, "@@ ") || strings.HasPrefix(trimmed, "--- ") || strings.HasPrefix(trimmed, "diff --git ") {
					break
				}
				if line == "\\ No newline at end of file" {
					index++
					continue
				}
				if line == "" && index == len(lines)-1 {
					index++
					break
				}
				if line == "" || line[0] != ' ' && line[0] != '+' && line[0] != '-' {
					break
				}
				hunk.lines = append(hunk.lines, line)
				index++
			}
			hunks = append(hunks, hunk)
		}
		if len(hunks) == 0 {
			return fmt.Errorf("patch for %s contains no unified hunks", path)
		}
		if err := apply_file_hunks(target, old_path == "/dev/null", new_path == "/dev/null", hunks); err != nil {
			return err
		}
	}
	return nil
}

// apply_file_hunks implements the apply file hunks operation.
func apply_file_hunks(path string, create, remove bool, hunks []unified_hunk) error {
	original, err := os.ReadFile(path)
	if err != nil && !create {
		return err
	}
	old_text := strings.ReplaceAll(string(original), "\r\n", "\n")
	old_lines := strings.Split(old_text, "\n")
	old_final_newline := strings.HasSuffix(old_text, "\n")
	if len(old_lines) > 0 && old_lines[len(old_lines)-1] == "" && old_final_newline {
		old_lines = old_lines[:len(old_lines)-1]
	}
	var updated []string
	cursor := 0
	for _, hunk := range hunks {
		expected := hunk.old_start - 1
		if hunk.old_start == 0 {
			expected = 0
		}
		if expected < cursor {
			expected = cursor
		}
		start := -1
		best_distance := len(old_lines) + 1
		for candidate := cursor; candidate <= len(old_lines); candidate++ {
			if !unified_hunk_matches(old_lines, candidate, hunk) {
				continue
			}
			distance := candidate - expected
			if distance < 0 {
				distance = -distance
			}
			if distance < best_distance {
				start, best_distance = candidate, distance
			}
		}
		if start < 0 {
			return fmt.Errorf("patch hunk does not match %s near line %d", path, expected+1)
		}
		updated = append(updated, old_lines[cursor:start]...)
		position := start
		for _, line := range hunk.lines {
			prefix, text := line[0], line[1:]
			switch prefix {
			case ' ':
				if position >= len(old_lines) || !patch_lines_equal(old_lines[position], text) {
					return fmt.Errorf("patch context does not match %s at line %d", path, position+1)
				}
				updated = append(updated, text)
				position++
			case '-':
				if position >= len(old_lines) || !patch_lines_equal(old_lines[position], text) {
					return fmt.Errorf("patch removal does not match %s at line %d", path, position+1)
				}
				position++
			case '+':
				updated = append(updated, text)
			}
		}
		cursor = position
	}
	updated = append(updated, old_lines[cursor:]...)
	if remove {
		return os.Remove(path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	result := strings.Join(updated, "\n")
	if old_final_newline || create {
		result += "\n"
	}
	return os.WriteFile(path, []byte(result), mode)
}

// unified_hunk_matches implements the unified hunk matches operation.
func unified_hunk_matches(source []string, start int, hunk unified_hunk) bool {
	position := start
	for _, line := range hunk.lines {
		prefix, expected := line[0], line[1:]
		if prefix != ' ' && prefix != '-' {
			continue
		}
		if position >= len(source) || !patch_lines_equal(source[position], expected) {
			return false
		}
		position++
	}
	return true
}

// patch_lines_equal implements the patch lines equal operation.
func patch_lines_equal(left, right string) bool {
	return strings.Join(strings.Fields(left), " ") == strings.Join(strings.Fields(right), " ")
}

// expand_at_files implements the expand at files operation.
func expand_at_files(args []string) ([]string, error) {
	var expanded []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "@") || len(arg) == 1 {
			expanded = append(expanded, arg)
			continue
		}
		data, err := os.ReadFile(strings.TrimPrefix(arg, "@"))
		if err != nil {
			return nil, err
		}
		for _, entry := range strings.Fields(string(data)) {
			expanded = append(expanded, entry)
		}
	}
	return expanded, nil
}

// run_local_nm implements the run local nm operation.
func run_local_nm(args []string) int {
	var input string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			input = arg
		}
	}
	if input == "" {
		fmt.Fprintln(os.Stderr, "hpe-nm: no input files")
		return 1
	}
	file, err := elf.Open(input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hpe-nm:", err)
		return 1
	}
	defer file.Close()
	symbols, err := file.Symbols()
	if err != nil && !errors.Is(err, elf.ErrNoSymbols) {
		fmt.Fprintln(os.Stderr, "hpe-nm:", err)
		return 1
	}
	for _, symbol := range symbols {
		if symbol.Name == "" {
			continue
		}
		kind := byte('N')
		switch {
		case symbol.Section == elf.SHN_UNDEF:
			kind = 'U'
		case symbol.Section == elf.SHN_ABS:
			kind = 'A'
		case symbol.Section == elf.SHN_COMMON:
			kind = 'C'
		case int(symbol.Section) < len(file.Sections):
			section := file.Sections[symbol.Section]
			switch {
			case section.Flags&elf.SHF_EXECINSTR != 0:
				kind = 'T'
			case section.Flags&elf.SHF_WRITE != 0:
				kind = 'D'
			case section.Flags&elf.SHF_ALLOC != 0:
				kind = 'R'
			default:
				kind = 'N'
			}
		}
		if elf.ST_BIND(symbol.Info) == elf.STB_LOCAL && kind >= 'A' && kind <= 'Z' {
			kind += 'a' - 'A'
		}
		if kind == 'U' {
			fmt.Printf("                 U %s\n", symbol.Name)
		} else {
			fmt.Printf("%016x %c %s\n", symbol.Value, kind, symbol.Name)
		}
	}
	return 0
}

// run_gperf_preprocessor implements the run gperf preprocessor operation.
func run_gperf_preprocessor(zig string, arguments []string) int {
	source_index := -1
	output_index := -1
	for i, arg := range arguments {
		if strings.HasSuffix(arg, ".gperf.h") {
			source_index = i
		}
		if arg == "-o" && i+1 < len(arguments) {
			output_index = i + 1
		}
	}
	if source_index < 0 {
		return 2
	}
	source, err := filepath.Abs(arguments[source_index])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	input, err := os.ReadFile(source)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	temp, err := os.MkdirTemp(filepath.Dir(source), "hpe-gperf-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer os.RemoveAll(temp)
	clean_source := filepath.Join(temp, filepath.Base(source))
	if err := os.WriteFile(clean_source, append(bytes.TrimRight(input, "\n"), '\n'), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	args := append([]string(nil), arguments...)
	args[source_index] = clean_source
	args = append(args, "-I", filepath.Dir(source))
	clean_output := filepath.Join(temp, "preprocessed.gperf")
	if output_index >= 0 {
		if out, err := filepath.Abs(arguments[output_index]); err == nil {
			clean_output = filepath.Join(temp, filepath.Base(out))
			args[output_index] = clean_output
		}
	}
	args = remove_argument(args, "-c")
	command := exec.Command(zig, append([]string{"cc"}, args...)...)
	var stdout bytes.Buffer
	if output_index < 0 {
		command.Stdout = &stdout
	} else {
		command.Stdout = os.Stdout
	}
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	generated := stdout.Bytes()
	if output_index >= 0 {
		generated, err = os.ReadFile(clean_output)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	filtered := filter_gperf_cutouts(string(generated))
	if !strings.Contains(filtered, "struct FcObjectTypeInfo {") {
		fmt.Fprintln(os.Stderr, "gperf preprocessing did not preserve struct FcObjectTypeInfo")
		return 1
	}
	marker := strings.Index(filtered, "struct FcObjectTypeInfo {")
	if !strings.Contains(filtered[marker:], "%%") {
		fmt.Fprintln(os.Stderr, "gperf preprocessing did not preserve the data separator")
		return 1
	}
	if output_index >= 0 {
		out, err := filepath.Abs(arguments[output_index])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		if err := os.WriteFile(out, []byte(filtered), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	} else {
		_, _ = io.WriteString(os.Stdout, filtered)
	}
	return 0
}

// remove_argument implements the remove argument operation.
func remove_argument(arguments []string, remove string) []string {
	result := make([]string, 0, len(arguments))
	for _, arg := range arguments {
		if arg != remove {
			result = append(result, arg)
		}
	}
	return result
}

// filter_gperf_cutouts implements the filter gperf cutouts operation.
func filter_gperf_cutouts(source string) string {
	var output strings.Builder
	in_cutout := false
	for _, line := range strings.Split(source, "\n") {
		switch strings.TrimSpace(line) {
		case "CUT_OUT_BEGIN":
			in_cutout = true
			continue
		case "CUT_OUT_END":
			in_cutout = false
			continue
		}
		if !in_cutout {
			output.WriteString(line)
			output.WriteByte('\n')
		}
	}
	return output.String()
}

// build_project implements the build project operation.
func build_project(paths layout) error {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return fmt.Errorf("building on %s is not supported", runtime.GOOS)
	}
	if err := run_step("Verify or download and install compiler SDKs and build tools", func() error { return ensure_tools(paths) }); err != nil {
		return err
	}
	cmake := filepath.Join(paths.tools, "cmake", "bin", "cmake")
	ninja := filepath.Join(paths.tools, "ninja", "ninja")
	zig := filepath.Join(paths.toolchain, "zig", "zig")
	dotnet := filepath.Join(paths.toolchain, "dotnet", "dotnet")
	if runtime.GOOS == "windows" {
		cmake += ".exe"
		ninja += ".exe"
		zig += ".exe"
		dotnet += ".exe"
	}
	if err := run_step("Prepare project-local compiler and Git compatibility wrappers", func() error { return install_zig_wrappers(paths) }); err != nil {
		return err
	}
	var ccache string
	if err := run_step("Prepare vcpkg dependency source and incremental ccache build", func() error {
		var err error
		ccache, err = prepare_ccache(paths, cmake, ninja, zig)
		return err
	}); err != nil {
		return err
	}
	jobs := build_jobs()
	return run_step("Configure, incrementally compile, and install HPE", func() error {
		return configure_build_install(paths, cmake, ninja, zig, dotnet, ccache, jobs)
	})
}

// build_jobs implements the build jobs operation.
func build_jobs() string {
	if value := os.Getenv("HPE_JOBS"); value != "" {
		return value
	}
	return fmt.Sprintf("%d", runtime.NumCPU())
}

// install_zig_wrappers implements the install zig wrappers operation.
func install_zig_wrappers(paths layout) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	bin := filepath.Join(paths.tools, "bootstrap")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"hpe-zig-cc", "hpe-zig-cxx", "hpe-zig-ar", "hpe-zig-ranlib", "hpe-zig-nm", "hpe-zig-ld", "git"} {
		wrapper := filepath.Join(bin, name)
		if runtime.GOOS == "windows" {
			wrapper += ".exe"
			if err := copy_file(executable, wrapper, 0o755); err != nil {
				return err
			}
			continue
		}
		_ = os.Remove(wrapper)
		if err := os.Symlink(executable, wrapper); err != nil {
			if err := copy_file(executable, wrapper, 0o755); err != nil {
				return err
			}
		}
	}
	return nil
}

// prepare_ccache implements the prepare ccache operation.
func prepare_ccache(paths layout, cmake, ninja, zig string) (string, error) {
	vcpkg := filepath.Join(paths.build, "vcpkg-root")
	if err := run_step("Verify or restore pinned vcpkg source", func() error {
		return ensure_pinned_source(paths, pinned_source{
			repository: "https://github.com/microsoft/vcpkg.git", revision: "9e593bb18ea69cc5095e012465dcd675a822ed0d",
			linux_url:      "https://codeload.github.com/microsoft/vcpkg/tar.gz/9e593bb18ea69cc5095e012465dcd675a822ed0d",
			linux_sha256:   "e9255b62719c659f224fcc639b1bf4a6226e4996ad3fb006e18e1533cad77725",
			windows_url:    "https://codeload.github.com/microsoft/vcpkg/zip/9e593bb18ea69cc5095e012465dcd675a822ed0d",
			windows_sha256: "92ea07cd3ad4b3bbe65e6e7088b4074eddab371d7bb70107c6e7ceefcb5d5c3e",
		}, vcpkg)
	}); err != nil {
		return "", err
	}
	if err := run_step("Prepare the pinned vcpkg executable", func() error { return bootstrap_vcpkg(vcpkg) }); err != nil {
		return "", err
	}
	if err := run_step("Prepare offline filesystem package registry", func() error { return prepare_filesystem_registry(vcpkg) }); err != nil {
		return "", err
	}
	if runtime.GOOS == "linux" {
		if err := run_step("Configure vcpkg to use the bundled Python runtime", func() error { return patch_vcpkg_python(vcpkg) }); err != nil {
			return "", err
		}
	}

	source := filepath.Join(paths.build, "ccache-source")
	if err := run_step("Verify or restore pinned ccache source", func() error {
		return ensure_pinned_source(paths, pinned_source{
			repository: "https://github.com/ccache/ccache.git", revision: "ec510e9b0f333b8e97aee98108de3e0858340d59",
			linux_url:      "https://codeload.github.com/ccache/ccache/tar.gz/ec510e9b0f333b8e97aee98108de3e0858340d59",
			linux_sha256:   "87d3ecc8dea48ab4e311e4fd24d4b1413caa9bde0c90965e04d9f7660e5b3c9c",
			windows_url:    "https://codeload.github.com/ccache/ccache/zip/ec510e9b0f333b8e97aee98108de3e0858340d59",
			windows_sha256: "f1263b96e9a8217f61caea184d59a374ccb4609d9d730d737bfa5567c837b76f",
		}, source)
	}); err != nil {
		return "", err
	}
	if err := run_step("Apply local ccache portability patch", func() error { return patch_ccache(source) }); err != nil {
		return "", err
	}
	build_dir := filepath.Join(paths.build, "ccache-build")
	prefix := filepath.Join(paths.tools, "ccache")
	ccache := filepath.Join(prefix, "bin", "ccache")
	if runtime.GOOS == "windows" {
		ccache += ".exe"
	}
	if executable_exists(ccache) {
		if output, err := exec.Command(ccache, "--version").CombinedOutput(); err == nil {
			fmt.Printf("Reusing validated ccache at %s: %s\n", ccache, strings.TrimSpace(string(output)))
			return ccache, nil
		}
		fmt.Printf("Installed ccache failed its version check; rebuilding: %s\n", ccache)
		if err := os.Remove(ccache); err != nil {
			return "", err
		}
	}
	env := compiler_environment(paths, zig, "", "")
	cc, cxx := wrapper_paths(paths)
	args := []string{"-S", source, "-B", build_dir, "-G", "Ninja", "-DCMAKE_MAKE_PROGRAM=" + ninja,
		"-DCMAKE_C_COMPILER=" + cc,
		"-DCMAKE_ASM_COMPILER=" + cc,
		"-DCMAKE_CXX_COMPILER=" + cxx,
		"-DCMAKE_POLICY_VERSION_MINIMUM=3.5", "-DZSTD_FROM_INTERNET=ON", "-DHIREDIS_FROM_INTERNET=OFF",
		"-DREDIS_STORAGE_BACKEND=OFF", "-DCCACHE_DEV_MODE=OFF", "-DBUILD_TESTING=OFF",
		"-DCMAKE_BUILD_TYPE=Release", "-DCMAKE_INSTALL_PREFIX=" + prefix}
	if err := run_step("Configure ccache with the bundled Zig toolchain", func() error { return run_command(paths.root, env, cmake, args...) }); err != nil {
		return "", err
	}
	if err := run_step("Compile ccache", func() error {
		return run_command(paths.root, env, cmake, "--build", build_dir, "--target", "ccache", "--parallel", build_jobs(), "--verbose")
	}); err != nil {
		return "", err
	}
	if err := run_step("Install ccache into the local build tools", func() error { return run_command(paths.root, env, cmake, "--install", build_dir) }); err != nil {
		return "", err
	}
	return ccache, nil
}

// ensure_pinned_source obtains the pinned revision using Git on Linux or a verified archive.
func ensure_pinned_source(paths layout, source pinned_source, destination string) error {
	marker := filepath.Join(destination, ".hpe-source-verified")
	archive_marker := "archive:" + source.windows_sha256
	if runtime.GOOS == "linux" {
		archive_marker = "archive:" + source.linux_sha256
	}
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile(marker); err == nil {
			marker_value := strings.TrimSpace(string(data))
			if marker_value == "git:"+source.revision || marker_value == archive_marker {
				fmt.Printf("Reusing verified source tree %s (%s)\n", destination, marker_value)
				return nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) == archive_marker {
		fmt.Printf("Reusing verified source tree %s (%s)\n", destination, archive_marker)
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Printf("Source tree is missing or has a stale verification marker; restoring %s\n", destination)
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		if git, err := find_host_git(paths); err == nil {
			if err := checkout_pinned_source(paths, git, source, destination); err == nil {
				return os.WriteFile(marker, []byte("git:"+source.revision+"\n"), 0o644)
			} else {
				fmt.Printf("Pinned Git checkout failed; falling back to the verified source archive: %v\n", err)
			}
		} else {
			fmt.Println("Git is unavailable; using the verified source archive to keep clean-host builds tool-independent")
		}
	}
	url, digest, format := source.windows_url, source.windows_sha256, "zip"
	if runtime.GOOS == "linux" {
		url, digest, format = source.linux_url, source.linux_sha256, "tar.gz"
	}
	archive := filepath.Join(paths.downloads, filepath.Base(destination)+"-"+digest[:12]+"."+format)
	if err := download_verified(url, archive, digest); err != nil {
		return err
	}
	if err := extract_archive(archive, destination, format, 1); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte("archive:"+digest+"\n"), 0o644)
}

// find_host_git locates system Git while excluding the bootstrap compatibility shim.
func find_host_git(paths layout) (string, error) {
	shim_root, err := filepath.Abs(filepath.Join(paths.tools, "bootstrap"))
	if err != nil {
		return "", err
	}
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if directory == "" {
			directory = "."
		}
		git, abs_err := filepath.Abs(filepath.Join(directory, "git"))
		if abs_err != nil {
			continue
		}
		if relative, rel_err := filepath.Rel(shim_root, git); rel_err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			continue
		}
		info, stat_err := os.Stat(git)
		if stat_err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return git, nil
		}
	}
	return "", errors.New("system Git is not available on PATH")
}

// checkout_pinned_source fetches one commit, removes Git metadata, and installs the source tree.
func checkout_pinned_source(paths layout, git string, source pinned_source, destination string) error {
	staging := destination + ".git-checkout"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	commands := [][]string{
		{"init", staging},
		{"-C", staging, "remote", "add", "origin", source.repository},
		{"-C", staging, "fetch", "--depth=1", "origin", source.revision},
		{"-C", staging, "checkout", "--detach", "FETCH_HEAD"},
	}
	for _, args := range commands {
		if err := run_command(paths.root, nil, git, args...); err != nil {
			return err
		}
	}
	command := exec.Command(git, "-C", staging, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("verify fetched Git revision: %w", err)
	}
	if revision := strings.TrimSpace(string(output)); revision != source.revision {
		return fmt.Errorf("Git returned revision %s, expected %s", revision, source.revision)
	}
	fmt.Printf("Verified Git checkout revision %s\n", source.revision)
	if err := os.RemoveAll(filepath.Join(staging, ".git")); err != nil {
		return err
	}
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	return os.Rename(staging, destination)
}

// bootstrap_vcpkg implements the bootstrap vcpkg operation.
func bootstrap_vcpkg(root string) error {
	name, asset, digest := "vcpkg", "", ""
	if runtime.GOOS == "windows" {
		name = "vcpkg.exe"
		if runtime.GOARCH == "arm64" {
			asset, digest = "vcpkg-arm64.exe", "5e8d7acd0f3049411ad1eacd3af2a946014ed430c27010e384ee1289a8e98cbc"
		} else {
			asset, digest = "vcpkg.exe", "13b8175e99a884c5ad34249218754b45541a1a63f216e92603aee57a285ac741"
		}
	} else if runtime.GOARCH == "arm64" {
		asset, digest = "vcpkg-arm64-glibc", "3907b4b11abafd5ee82c04721bff80a4ca865c9f1d1ed0b62eb8f43f4470f313b3f6d19779e70b37db2678a6bd0536e3c1d8d542542e75c00f80d97932a9755a"
	} else {
		asset, digest = "vcpkg-glibc", "be59d1fdf3725d2fb4bd1c0b435266726aaff2f87cb2503b775f44fb9b392ab474e6370d7e90e9d07b2c584b7deacc0670f98b477905c2e0d5cba5e01cee93dc"
	}
	path := filepath.Join(root, name)
	url := "https://github.com/microsoft/vcpkg-tool/releases/download/2026-07-27/" + asset
	if err := download_verified(url, path, digest); err != nil {
		return err
	}
	fmt.Printf("Verified the local vcpkg executable at %s\n", path)
	if runtime.GOOS != "windows" {
		return os.Chmod(path, 0o755)
	}
	return nil
}

// patch_ccache implements the patch ccache operation.
func patch_ccache(source string) error {
	path := filepath.Join(source, "CMakeLists.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated := strings.Replace(string(data), "project(ccache LANGUAGES C CXX ASM ASM_MASM)", "project(ccache LANGUAGES C CXX ASM)", 1)
	if updated == string(data) && !strings.Contains(updated, "project(ccache LANGUAGES C CXX ASM)") {
		return errors.New("ccache CMake project declaration has changed; update the local patch")
	}
	return os.WriteFile(path, []byte(updated), 0o644)
}

// prepare_filesystem_registry implements the prepare filesystem registry operation.
func prepare_filesystem_registry(root string) error {
	baseline_path := filepath.Join(root, "versions", "baseline.json")
	data, err := os.ReadFile(baseline_path)
	if err != nil {
		return err
	}
	var baselines map[string]map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &baselines); err != nil {
		return err
	}
	baseline, ok := baselines["default"]
	if !ok {
		return errors.New("vcpkg filesystem registry source is missing the default baseline")
	}
	for name, version := range baseline {
		var baseline_version string
		if err := json.Unmarshal(version["baseline"], &baseline_version); err != nil {
			return fmt.Errorf("parse vcpkg baseline for %s: %w", name, err)
		}
		var baseline_port_version int
		if err := json.Unmarshal(version["port-version"], &baseline_port_version); err != nil {
			return fmt.Errorf("parse vcpkg port version for %s: %w", name, err)
		}
		version_file := filepath.Join(root, "versions", string(name[0])+"-", name+".json")
		data, err := os.ReadFile(version_file)
		if err != nil {
			return fmt.Errorf("read vcpkg version file for %s: %w", name, err)
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal(data, &record); err != nil {
			return fmt.Errorf("parse vcpkg version file for %s: %w", name, err)
		}
		var versions []map[string]json.RawMessage
		if err := json.Unmarshal(record["versions"], &versions); err != nil {
			return fmt.Errorf("parse vcpkg versions for %s: %w", name, err)
		}
		var selected map[string]json.RawMessage
		for _, candidate := range versions {
			for _, key := range []string{"version", "version-semver", "version-date", "version-string"} {
				var candidate_version string
				if raw := candidate[key]; len(raw) != 0 && json.Unmarshal(raw, &candidate_version) == nil && candidate_version == baseline_version {
					var candidate_port_version int
					_ = json.Unmarshal(candidate["port-version"], &candidate_port_version)
					if candidate_port_version == baseline_port_version {
						selected = candidate
					}
				}
			}
		}
		if selected == nil {
			return fmt.Errorf("vcpkg version baseline for %s (%s#%d) is absent from its version file", name, baseline_version, baseline_port_version)
		}
		delete(selected, "git-tree")
		selected["path"], _ = json.Marshal("$/ports/" + name)
		output, err := json.MarshalIndent(map[string]any{"versions": []map[string]json.RawMessage{selected}}, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(version_file, append(output, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// patch_vcpkg_python implements the patch vcpkg python operation.
func patch_vcpkg_python(root string) error {
	path := filepath.Join(root, "scripts", "cmake", "vcpkg_find_acquire_program.cmake")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	marker := "include(\"${program_information}\")"
	patch := `include("${program_information}")
        if(program STREQUAL "PYTHON3" AND DEFINED ENV{HPE_PYTHON3} AND EXISTS "$ENV{HPE_PYTHON3}")
            get_filename_component(hpe_python_dir "$ENV{HPE_PYTHON3}" DIRECTORY)
            set(paths_to_search "${hpe_python_dir}")
            set(search_names python3)
        endif()`
	if !strings.Contains(text, marker) {
		if strings.Contains(text, "HPE_PYTHON3") {
			return nil
		}
		return errors.New("vcpkg Python acquisition hook has changed; update the patch")
	}
	return os.WriteFile(path, []byte(strings.Replace(text, marker, patch, 1)), 0o644)
}

// compiler_environment implements the compiler environment operation.
func compiler_environment(paths layout, zig, ccache, python string) map[string]string {
	env := map[string]string{"HPE_ZIG": zig, "CCACHE_DIR": filepath.Join(paths.build, "ccache-cache"),
		"CCACHE_BASEDIR": paths.root, "CCACHE_COMPILERCHECK": "content", "VCPKG_DISABLE_METRICS": "1",
		"VCPKG_DOWNLOADS": filepath.Join(paths.build, "vcpkg-downloads"), "VCPKG_BINARY_SOURCES": "clear",
		"HPE_VCPKG_ROOT":        filepath.Join(paths.build, "vcpkg-root"),
		"VCPKG_MAX_CONCURRENCY": build_jobs(), "PATH": local_build_path(paths)}
	if ccache != "" {
		env["HPE_CCACHE"] = ccache
	}
	if python != "" {
		env["HPE_PYTHON3"] = python
	}
	return env
}

// local_build_path implements the local build path operation.
func local_build_path(paths layout) string {
	parts := []string{
		filepath.Join(paths.tools, "build-tools", "bin"),
		filepath.Join(paths.tools, "cmake", "bin"),
		filepath.Join(paths.tools, "ninja"),
		filepath.Join(paths.tools, "bootstrap"),
		filepath.Join(paths.tools, "ccache", "bin"),
		filepath.Join(paths.toolchain, "zig"),
		filepath.Join(paths.toolchain, "dotnet"),
	}
	if runtime.GOOS == "linux" {
		parts = append(parts, filepath.Join(paths.tools, "python", "bin"))
	} else if windows_dir := os.Getenv("WINDIR"); windows_dir != "" {
		parts = append(parts, filepath.Join(windows_dir, "System32"))
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

// ensure_linux_host_tools implements the ensure linux host tools operation.
func ensure_linux_host_tools(paths layout, jobs string) error {
	zig := filepath.Join(paths.toolchain, "zig", "zig")
	prefix := filepath.Join(paths.tools, "build-tools")
	sources := filepath.Join(paths.build, "host-tools", "src")
	builds := filepath.Join(paths.build, "host-tools", "build")
	downloads := filepath.Join(paths.build, "downloads", "build-tools")
	marker := filepath.Join(prefix, ".hpe-prefix")
	valid_prefix := false
	if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) == prefix {
		for _, relative := range []string{"bin/aclocal", "bin/autoconf", "bin/automake", "bin/libtoolize"} {
			program, read_err := os.ReadFile(filepath.Join(prefix, filepath.FromSlash(relative)))
			if read_err == nil && bytes.Contains(program, []byte(prefix)) {
				valid_prefix = true
				break
			}
		}
	}
	if !valid_prefix {
		if err := os.RemoveAll(prefix); err != nil {
			return err
		}
		if err := os.RemoveAll(builds); err != nil {
			return err
		}
	}
	for _, directory := range []string{prefix, sources, builds, downloads} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
	}
	if err := install_linux_base_tools(prefix, downloads); err != nil {
		return err
	}
	if valid_prefix && executable_exists(filepath.Join(prefix, "bin", "gperf")) {
		if err := rewrite_interpreter_shebangs(prefix, prefix); err != nil {
			return err
		}
		return nil
	}
	packages := []struct{ name, archive, hash, url, format string }{
		{"m4", "m4-1.4.21.tar.xz", "f25c6ab51548a73a75558742fb031e0625d6485fe5f9155949d6486a2408ab66", "https://ftp.gnu.org/gnu/m4/m4-1.4.21.tar.xz", "tar.xz"},
		{"autoconf", "autoconf-2.73.tar.xz", "9fd672b1c8425fac2fa67fa0477b990987268b90ff36d5f016dae57be0d6b52e", "https://ftp.gnu.org/gnu/autoconf/autoconf-2.73.tar.xz", "tar.xz"},
		{"automake", "automake-1.18.1.tar.xz", "168aa363278351b89af56684448f525a5bce5079d0b6842bd910fdd3f1646887", "https://ftp.gnu.org/gnu/automake/automake-1.18.1.tar.xz", "tar.xz"},
		{"libtool", "libtool-2.5.4.tar.xz", "f81f5860666b0bc7d84baddefa60d1cb9fa6fceb2398cc3baca6afaa60266675", "https://ftp.gnu.org/gnu/libtool/libtool-2.5.4.tar.xz", "tar.xz"},
		{"autoconf-archive", "autoconf-archive-2024.10.16.tar.xz", "7bcd5d001916f3a50ed7436f4f700e3d2b1bade3ed803219c592d62502a57363", "https://ftp.gnu.org/gnu/autoconf-archive/autoconf-archive-2024.10.16.tar.xz", "tar.xz"},
		{"bison", "bison-3.8.2.tar.xz", "9bba0214ccf7f1079c5d59210045227bcf619519840ebfa80cd3849cff5a5bf2", "https://ftp.gnu.org/gnu/bison/bison-3.8.2.tar.xz", "tar.xz"},
		{"flex", "flex-2.6.4.tar.gz", "e87aae032bf07c26f85ac0ed3250998c37621d95f8bd748b31f15b33c45ee995", "https://github.com/westes/flex/releases/download/v2.6.4/flex-2.6.4.tar.gz", "tar.gz"},
		{"gperf-3.1", "gperf-3.1.tar.gz", "588546b945bba4b70b6a3a616e80b4ab466e3f33024a352fc2198112cdbb3ae2", "https://ftp.gnu.org/pub/gnu/gperf/gperf-3.1.tar.gz", "tar.gz"},
	}
	for _, pkg := range packages {
		archive := filepath.Join(downloads, pkg.archive)
		if err := download_verified(pkg.url, archive, pkg.hash); err != nil {
			return err
		}
		if err := extract_host_tool(archive, filepath.Join(sources, pkg.name), pkg.format, pkg.hash); err != nil {
			return err
		}
		if err := rewrite_interpreter_shebangs(filepath.Join(sources, pkg.name), prefix); err != nil {
			return err
		}
	}
	if err := install_conda_make(paths, prefix, downloads); err != nil {
		return err
	}
	pkg_m4 := filepath.Join(downloads, "pkg.m4")
	if err := download_verified("https://raw.githubusercontent.com/pkgconf/pkgconf/master/pkg.m4", pkg_m4,
		"f649256076fa72946d9a844636393128e1c31fd3591389b97aa55705c4df5766"); err != nil {
		return err
	}
	if err := copy_file(pkg_m4, filepath.Join(prefix, "share", "aclocal", "pkg.m4"), 0o644); err != nil {
		return err
	}
	local_make := filepath.Join(prefix, "bin", "make")
	if err := configure_host_tool(paths, prefix, sources, builds, "m4", jobs, nil); err != nil {
		return err
	}
	if err := configure_host_tool(paths, prefix, sources, builds, "autoconf", jobs, map[string]string{"M4": filepath.Join(prefix, "bin", "m4")}); err != nil {
		return err
	}
	if err := configure_host_tool(paths, prefix, sources, builds, "automake", jobs, nil); err != nil {
		return err
	}
	if err := configure_host_tool(paths, prefix, sources, builds, "libtool", jobs, nil); err != nil {
		return err
	}
	archive_build := filepath.Join(builds, "autoconf-archive")
	if _, err := os.Stat(filepath.Join(prefix, "share", "aclocal", "ax_pthread.m4")); err != nil {
		if err := os.MkdirAll(archive_build, 0o755); err != nil {
			return err
		}
		if err := run_local_shell(archive_build, host_tool_environment(paths, zig, prefix), prefix, filepath.Join(sources, "autoconf-archive", "configure"), "--prefix="+prefix); err != nil {
			return err
		}
		if err := rewrite_interpreter_shebangs(archive_build, prefix); err != nil {
			return err
		}
		if err := run_local_make(archive_build, host_tool_environment(paths, zig, prefix), prefix, local_make, "-j"+jobs, "V=1"); err != nil {
			return err
		}
		if err := run_local_make(archive_build, host_tool_environment(paths, zig, prefix), prefix, local_make, "install"); err != nil {
			return err
		}
	}
	for _, name := range []string{"bison", "flex", "gperf-3.1"} {
		binary := name
		if name == "gperf-3.1" {
			binary = "gperf"
		}
		if executable_exists(filepath.Join(prefix, "bin", binary)) {
			continue
		}
		build_dir := filepath.Join(builds, name)
		if err := os.MkdirAll(build_dir, 0o755); err != nil {
			return err
		}
		configure_env := host_tool_environment(paths, zig, prefix)
		args := []string{"--prefix=" + prefix}
		if name == "bison" {
			configure_env["M4"] = filepath.Join(prefix, "bin", "m4")
			args = append(args, "--disable-nls")
		} else if name == "flex" {
			configure_env["M4"] = filepath.Join(prefix, "bin", "m4")
			args = append(args, "--disable-nls")
		} else {
			configure_env["CXXFLAGS"] = "-O2 -std=gnu++14"
		}
		if err := run_local_shell(build_dir, configure_env, prefix, filepath.Join(sources, name, "configure"), args...); err != nil {
			return err
		}
		if err := rewrite_interpreter_shebangs(build_dir, prefix); err != nil {
			return err
		}
		if err := run_local_make(build_dir, host_tool_environment(paths, zig, prefix), prefix, local_make, "-j"+jobs, "V=1"); err != nil {
			return err
		}
		if err := run_local_make(build_dir, host_tool_environment(paths, zig, prefix), prefix, local_make, "install"); err != nil {
			return err
		}
	}
	if err := os.WriteFile(marker, []byte(prefix+"\n"), 0o644); err != nil {
		return err
	}
	if err := rewrite_interpreter_shebangs(prefix, prefix); err != nil {
		return err
	}
	fmt.Printf("Project-local GNU build helpers installed in %s\n", prefix)
	return nil
}

// install_linux_base_tools implements the install linux base tools operation.
func install_linux_base_tools(prefix, downloads string) error {
	busybox_name, busybox_hash := "busybox-static_1.38.0-3+b1_amd64.deb", "26801f17e6c88e813be104effc0ea3b43d912bd59ca6295fcd1260528ebb4d41"
	perl_arch, perl_hash := "amd64", "a9040a81c2c9b2f5e03f39d444ecc6b86e9682f291d54b164b3ef093531f802c"
	if runtime.GOARCH == "arm64" {
		busybox_name, busybox_hash = "busybox-static_1.38.0-3+b1_arm64.deb", "968d1aa8f579fa1ac59c26afa365454369e13cf29848e6400b50028fed0ffda0"
		perl_arch, perl_hash = "arm64", "b7ecde2320be2ce292db5886894954fd4b10738bed69d4122eec27d46656151a"
	}
	bin := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	busybox := filepath.Join(bin, "busybox")
	busybox_valid := false
	if executable_exists(busybox) {
		if output, err := exec.Command(busybox, "--list").Output(); err == nil && len(output) > 0 {
			busybox_valid = true
			fmt.Printf("Reusing validated static BusyBox from %s\n", busybox)
		} else {
			fmt.Printf("Static BusyBox failed its integrity check; restoring from the verified Debian package\n")
			if err := os.Remove(busybox); err != nil {
				return err
			}
		}
	}
	if !busybox_valid {
		deb := filepath.Join(downloads, busybox_name)
		url := "https://deb.debian.org/debian/pool/main/b/busybox/" + busybox_name
		if err := download_verified(url, deb, busybox_hash); err != nil {
			return fmt.Errorf("download static BusyBox: %w", err)
		}
		if err := extract_debian_data_tar(deb, prefix); err != nil {
			return fmt.Errorf("extract static BusyBox: %w", err)
		}
		candidate := filepath.Join(prefix, "usr", "bin", "busybox")
		if _, err := os.Stat(candidate); err == nil {
			if err := os.MkdirAll(bin, 0o755); err != nil {
				return err
			}
			if err := os.Rename(candidate, busybox); err != nil {
				return err
			}
		}
		if !executable_exists(busybox) {
			return errors.New("Debian BusyBox package did not contain usr/bin/busybox")
		}
	}
	applets, err := exec.Command(busybox, "--list").Output()
	if err != nil {
		return fmt.Errorf("list BusyBox applets: %w", err)
	}
	for _, applet := range strings.Fields(string(applets)) {
		if applet == "ar" || applet == "make" {
			continue
		}
		link := filepath.Join(bin, applet)
		if _, err := os.Lstat(link); errors.Is(err, os.ErrNotExist) {
			if err := os.Symlink("busybox", link); err != nil {
				return err
			}
		}
	}
	perl_dir := filepath.Join(prefix, "perl")
	perl := filepath.Join(perl_dir, "bin", "perl")
	perl_valid := false
	if executable_exists(perl) {
		if output, err := exec.Command(perl, "--version").CombinedOutput(); err == nil && bytes.Contains(output, []byte("Perl")) {
			perl_valid = true
			fmt.Printf("Reusing validated relocatable Perl from %s\n", perl)
		} else {
			fmt.Printf("Relocatable Perl failed its version check; restoring from the verified archive\n")
			if err := os.Remove(perl); err != nil {
				return err
			}
		}
	}
	if !perl_valid {
		archive_name := "perl-linux-" + perl_arch + ".tar.xz"
		archive := filepath.Join(downloads, archive_name)
		url := "https://github.com/skaji/relocatable-perl/releases/download/5.44.0.0/" + archive_name
		if err := download_verified(url, archive, perl_hash); err != nil {
			return fmt.Errorf("download relocatable Perl: %w", err)
		}
		staging := perl_dir + ".staging"
		_ = os.RemoveAll(staging)
		if err := os.MkdirAll(staging, 0o755); err != nil {
			return err
		}
		if err := extract_tar_xz(archive, staging, 1); err != nil {
			return err
		}
		if !executable_exists(filepath.Join(staging, "bin", "perl")) {
			return errors.New("relocatable Perl archive did not contain bin/perl")
		}
		if err := replace_directory(staging, perl_dir); err != nil {
			return err
		}
	}
	perl_link := filepath.Join(bin, "perl")
	if _, err := os.Lstat(perl_link); errors.Is(err, os.ErrNotExist) {
		if err := os.Symlink(filepath.Join("..", "perl", "bin", "perl"), perl_link); err != nil {
			return err
		}
	}
	return nil
}

// rewrite_interpreter_shebangs implements the rewrite interpreter shebangs operation.
func rewrite_interpreter_shebangs(root, prefix string) error {
	marker := filepath.Join(root, ".hpe-interpreters-patched")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	shell := filepath.Join(prefix, "bin", "sh")
	perl := filepath.Join(prefix, "bin", "perl")
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walk_err error) error {
		if walk_err != nil || entry.IsDir() {
			return walk_err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		line, read_err := io.ReadAll(io.LimitReader(file, 256))
		close_err := file.Close()
		if read_err != nil {
			return read_err
		}
		if close_err != nil {
			return close_err
		}
		end := bytes.IndexByte(line, '\n')
		if end < 0 || !bytes.HasPrefix(line, []byte("#!")) {
			return nil
		}
		first := string(line[:end])
		var replacement string
		switch {
		case strings.Contains(first, "/bin/sh"), strings.Contains(first, "/bin/bash"), strings.Contains(first, "/usr/bin/env sh"), strings.Contains(first, "/usr/bin/env bash"):
			replacement = "#!" + shell
		case strings.Contains(first, "/usr/bin/perl"), strings.Contains(first, "/usr/bin/env perl"):
			replacement = "#!" + perl
		default:
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if err := os.Chmod(path, mode|0o200); err != nil {
			return err
		}
		if err := os.WriteFile(path, append([]byte(replacement), content[end:]...), mode|0o200); err != nil {
			return err
		}
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
		return os.Chtimes(path, info.ModTime(), info.ModTime())
	}); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte("ok\n"), 0o644)
}

// run_local_shell implements the run local shell operation.
func run_local_shell(directory string, env map[string]string, prefix, script string, args ...string) error {
	return run_command(directory, env, filepath.Join(prefix, "bin", "sh"), append([]string{script}, args...)...)
}

// run_local_make implements the run local make operation.
func run_local_make(directory string, env map[string]string, prefix, make string, args ...string) error {
	args = append(args, "SHELL="+filepath.Join(prefix, "bin", "sh"))
	return run_command(directory, env, make, args...)
}

// extract_debian_data_tar implements the extract debian data tar operation.
func extract_debian_data_tar(deb, destination string) error {
	data, err := os.ReadFile(deb)
	if err != nil {
		return err
	}
	if len(data) < 8 || string(data[:8]) != "!<arch>\n" {
		return errors.New("invalid Debian archive header")
	}
	for offset := 8; offset+60 <= len(data); {
		header := data[offset : offset+60]
		if string(header[58:60]) != "`\n" {
			return errors.New("invalid Debian archive member header")
		}
		size_text := strings.TrimSpace(string(header[48:58]))
		var size int
		if _, err := fmt.Sscanf(size_text, "%d", &size); err != nil || size < 0 || offset+60+size > len(data) {
			return errors.New("invalid Debian archive member size")
		}
		name := strings.TrimSpace(strings.TrimSuffix(string(header[:16]), "/"))
		member := data[offset+60 : offset+60+size]
		if strings.HasPrefix(name, "data.tar") {
			var reader io.Reader = bytes.NewReader(member)
			switch {
			case strings.HasSuffix(name, ".xz"):
				xz_reader, err := xz.NewReader(reader)
				if err != nil {
					return err
				}
				reader = xz_reader
			case strings.HasSuffix(name, ".gz"):
				gz_reader, err := gzip.NewReader(reader)
				if err != nil {
					return err
				}
				defer gz_reader.Close()
				reader = gz_reader
			default:
				return fmt.Errorf("unsupported Debian data archive %s", name)
			}
			return extract_tar(reader, destination, 0)
		}
		offset += 60 + size
		if offset%2 != 0 {
			offset++
		}
	}
	return errors.New("Debian archive has no data.tar member")
}

// host_tool_environment implements the host tool environment operation.
func host_tool_environment(paths layout, zig, prefix string) map[string]string {
	cc, cxx := wrapper_paths(paths)
	zig_ar := filepath.Join(paths.tools, "bootstrap", "hpe-zig-ar")
	zig_ranlib := filepath.Join(paths.tools, "bootstrap", "hpe-zig-ranlib")
	zig_nm := filepath.Join(paths.tools, "bootstrap", "hpe-zig-nm")
	zig_ld := filepath.Join(paths.tools, "bootstrap", "hpe-zig-ld")
	return map[string]string{"CC": cc, "CXX": cxx, "CC_FOR_BUILD": cc, "CXX_FOR_BUILD": cxx,
		"AR": zig_ar, "RANLIB": zig_ranlib, "NM": zig_nm, "LD": zig_ld, "HPE_ZIG": zig,
		"CFLAGS": "-O2", "CXXFLAGS": "-O2", "AWK": filepath.Join(prefix, "bin", "awk"),
		"GREP": filepath.Join(prefix, "bin", "grep"), "EGREP": filepath.Join(prefix, "bin", "grep") + " -E",
		"FGREP": filepath.Join(prefix, "bin", "grep") + " -F", "TAR": filepath.Join(prefix, "bin", "tar"),
		"PATH": filepath.Join(prefix, "bin"),
		"PERL": filepath.Join(prefix, "bin", "perl"), "CONFIG_SHELL": filepath.Join(prefix, "bin", "sh"),
		"SHELL": filepath.Join(prefix, "bin", "sh"), "ACLOCAL_PATH": filepath.Join(prefix, "share", "aclocal")}
}

// configure_host_tool implements the configure host tool operation.
func configure_host_tool(paths layout, prefix, sources, builds, name, jobs string, extra map[string]string) error {
	binary := name
	if name == "libtool" {
		binary = "libtoolize"
	}
	if executable_exists(filepath.Join(prefix, "bin", binary)) {
		fmt.Printf("Reusing installed Linux host tool: %s\n", filepath.Join(prefix, "bin", binary))
		return nil
	}
	build_dir := filepath.Join(builds, name)
	if err := os.MkdirAll(build_dir, 0o755); err != nil {
		return err
	}
	env := host_tool_environment(paths, filepath.Join(paths.toolchain, "zig", "zig"), prefix)
	for key, value := range extra {
		env[key] = value
	}
	args := []string{"--prefix=" + prefix}
	if name == "m4" {
		args = append(args, "--with-syscmd-shell="+filepath.Join(prefix, "bin", "sh"))
	}
	if err := run_step("Configure Linux host tool "+name, func() error {
		return run_local_shell(build_dir, env, prefix, filepath.Join(sources, name, "configure"), args...)
	}); err != nil {
		return err
	}
	make := filepath.Join(prefix, "bin", "make")
	if err := rewrite_interpreter_shebangs(build_dir, prefix); err != nil {
		return err
	}
	if err := run_step("Compile Linux host tool "+name, func() error { return run_local_make(build_dir, env, prefix, make, "-j"+jobs, "V=1") }); err != nil {
		return err
	}
	return run_step("Install Linux host tool "+name, func() error { return run_local_make(build_dir, env, prefix, make, "install") })
}

// extract_host_tool implements the extract host tool operation.
func extract_host_tool(archive, destination, format, digest string) error {
	marker := filepath.Join(destination, ".hpe-extracted")
	if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) == digest {
		fmt.Printf("Reusing extracted host tool source %s (SHA-256 %s)\n", destination, digest)
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Printf("Extracting verified host tool source into %s\n", destination)
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	var err error
	switch format {
	case "tar.xz":
		err = extract_tar_xz(archive, destination, 1)
	case "tar.gz":
		err = extract_tar_gz(archive, destination, 1)
	default:
		err = fmt.Errorf("unsupported host tool archive format %q", format)
	}
	if err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(digest+"\n"), 0o644)
}

// install_conda_make implements the install conda make operation.
func install_conda_make(paths layout, prefix, downloads string) error {
	installed := filepath.Join(prefix, "bin", "make")
	if executable_exists(installed) {
		if output, err := exec.Command(installed, "--version").CombinedOutput(); err == nil && bytes.Contains(output, []byte("GNU Make")) {
			fmt.Printf("Reusing validated GNU Make from %s\n", installed)
			return nil
		}
		fmt.Printf("GNU Make failed its version check; restoring from the verified package\n")
		if err := os.Remove(installed); err != nil {
			return err
		}
	}
	arch, filename, digest := "64", "make-4.4.1-h1114479_4.conda", "d6a81e5c142d7be6a992f618097351e7996f81a6295045df3f9b4f498e1066a8"
	if runtime.GOARCH == "arm64" {
		arch, filename, digest = "aarch64", "make-4.4.1-hfbd14cd_4.conda", "d2accef9d131077ad74894e05e533038e843739bd8bc3cb97a209d9f49bd176c"
	}
	url := "https://api.anaconda.org/download/conda-forge/make/4.4.1/linux-" + arch + "/" + filename
	archive := filepath.Join(downloads, filename)
	if err := download_verified(url, archive, digest); err != nil {
		return fmt.Errorf("download GNU Make: %w", err)
	}
	package_archive, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer package_archive.Close()
	for _, entry := range package_archive.File {
		if !strings.HasPrefix(entry.Name, "pkg-") || !strings.HasSuffix(entry.Name, ".tar.zst") {
			continue
		}
		compressed, err := entry.Open()
		if err != nil {
			return err
		}
		decoder, err := zstd.NewReader(compressed)
		if err != nil {
			_ = compressed.Close()
			return err
		}
		err = extract_tar(decoder, prefix, 0)
		decoder.Close()
		_ = compressed.Close()
		if err != nil {
			return err
		}
		if !executable_exists(installed) {
			return errors.New("GNU Make package did not contain bin/make")
		}
		return nil
	}
	return errors.New("GNU Make package did not contain a compressed package archive")
}

// configure_build_install implements the configure build install operation.
func configure_build_install(paths layout, cmake, ninja, zig, dotnet, ccache string, jobs string) error {
	rid := host_runtime_id()
	install_root := env_path("HPE_INSTALL_ROOT", filepath.Join(paths.root, "install", rid))
	project_build := filepath.Join(paths.build, "projects", rid, "Release")
	fmt.Printf("Target runtime: %s; parallel jobs: %s; incremental CMake/Ninja directory: %s\n", rid, jobs, project_build)
	var manifest_dir string
	if err := run_step("Prepare vcpkg manifest and local registry configuration", func() error {
		var err error
		manifest_dir, err = prepare_vcpkg_manifest(paths)
		return err
	}); err != nil {
		return err
	}
	cc, cxx := wrapper_paths(paths)
	python := ""
	if runtime.GOOS == "linux" {
		python = filepath.Join(paths.tools, "python", "bin", "python3")
	}
	env := compiler_environment(paths, zig, ccache, python)
	if runtime.GOARCH == "arm64" {
		env["HPE_VCPKG_ARCH"] = "arm64"
	} else {
		env["HPE_VCPKG_ARCH"] = "x64"
	}
	env["HPE_CC"], env["HPE_CXX"] = cc, cxx
	env["CC"], env["CXX"] = cc, cxx
	env["PATH"] = local_build_path(paths)
	if runtime.GOOS == "linux" {
		env["ACLOCAL_PATH"] = filepath.Join(paths.tools, "build-tools", "share", "aclocal")
		env["HPE_GPERF"] = filepath.Join(paths.tools, "build-tools", "bin", "gperf")
	}
	if err := os.MkdirAll(filepath.Join(paths.build, "ccache-cache"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(paths.build, "vcpkg-downloads"), 0o755); err != nil {
		return err
	}
	triplet := "hpe-linux"
	if runtime.GOOS == "windows" {
		triplet = "hpe-windows"
	}
	vcpkg := filepath.Join(paths.build, "vcpkg-root")
	args := []string{"-S", paths.root, "-B", project_build, "-G", "Ninja", "-DCMAKE_MAKE_PROGRAM=" + ninja,
		"-DCMAKE_BUILD_TYPE=Release", "-DCMAKE_C_COMPILER=" + cc, "-DCMAKE_C_COMPILER_LAUNCHER=" + ccache,
		"-DVCPKG_APPLOCAL_DEPS=ON", "-DVCPKG_USE_LEGACY_APPLOCAL=OFF",
		"-DCMAKE_TOOLCHAIN_FILE=" + filepath.Join(vcpkg, "scripts", "buildsystems", "vcpkg.cmake"),
		"-DVCPKG_ROOT=" + vcpkg, "-DVCPKG_TARGET_TRIPLET=" + triplet, "-DVCPKG_HOST_TRIPLET=" + triplet,
		"-DVCPKG_OVERLAY_TRIPLETS=" + filepath.Join(paths.root, "cmake", "vcpkg", "triplets"),
		"-DVCPKG_OVERLAY_PORTS=" + filepath.Join(paths.root, "cmake", "vcpkg", "overlay-ports"),
		"-DVCPKG_MANIFEST_DIR=" + manifest_dir,
		"-DVCPKG_INSTALLED_DIR=" + filepath.Join(project_build, "vcpkg_installed"),
		"-DVCPKG_MANIFEST_INSTALL=ON", "-DVCPKG_MANIFEST_MODE=ON", "-DCMAKE_INSTALL_PREFIX=" + install_root}
	if err := run_step("Configure HPE and resolve pinned native dependencies", func() error { return run_command(paths.root, env, cmake, args...) }); err != nil {
		return err
	}
	if err := run_step("Incrementally compile native simulator, CLI, and GUI bridge", func() error {
		return run_command(paths.root, env, cmake, "--build", project_build, "--parallel", jobs, "--verbose")
	}); err != nil {
		return err
	}
	if err := run_step("Refresh native install files", func() error {
		if err := os.RemoveAll(install_root); err != nil {
			return err
		}
		if err := os.MkdirAll(install_root, 0o755); err != nil {
			return err
		}
		return run_command(paths.root, env, cmake, "--install", project_build)
	}); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		if err := run_step("Bundle compiler runtime libraries for Linux", func() error { return bundle_linux_runtime(zig, install_root) }); err != nil {
			return err
		}
	}
	if err := run_step("Incrementally publish the self-contained desktop GUI", func() error {
		return publish_gui(paths, dotnet, install_root, rid, jobs)
	}); err != nil {
		return err
	}
	if err := run_step("Copy protocol examples and bundled fonts", func() error {
		if err := copy_directory(filepath.Join(paths.root, "examples"), filepath.Join(install_root, "gui", "examples")); err != nil {
			return err
		}
		return copy_directory(filepath.Join(paths.root, "assets", "fonts"), filepath.Join(install_root, "lib", "fonts"))
	}); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		return run_step("Write Linux GUI launcher", func() error { return write_linux_launcher(install_root) })
	}
	launcher := []byte("@echo off\r\nset ROOT=%~dp0\r\n\"%ROOT%gui\\hpe-gui.exe\" %*\r\n")
	return run_step("Write Windows GUI launcher", func() error { return os.WriteFile(filepath.Join(install_root, "hpe-gui.cmd"), launcher, 0o644) })
}

// host_runtime_id implements the host runtime id operation.
func host_runtime_id() string {
	platform := "linux"
	if runtime.GOOS == "windows" {
		platform = "win"
	}
	arch := "x64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	return platform + "-" + arch
}

// prepare_vcpkg_manifest implements the prepare vcpkg manifest operation.
func prepare_vcpkg_manifest(paths layout) (string, error) {
	manifest_dir := filepath.Join(paths.build, "manifests")
	if err := os.MkdirAll(manifest_dir, 0o755); err != nil {
		return "", err
	}
	manifest, err := os.ReadFile(filepath.Join(paths.root, "vcpkg.json"))
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(manifest_dir, "vcpkg.json"), manifest, 0o644); err != nil {
		return "", err
	}
	relative_registry, err := filepath.Rel(manifest_dir, filepath.Join(paths.build, "vcpkg-root"))
	if err != nil {
		return "", err
	}
	configuration := map[string]any{"default-registry": map[string]string{
		"kind": "filesystem", "path": filepath.ToSlash(relative_registry)}}
	data, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		return "", err
	}
	return manifest_dir, os.WriteFile(filepath.Join(manifest_dir, "vcpkg-configuration.json"), append(data, '\n'), 0o644)
}

// wrapper_paths implements the wrapper paths operation.
func wrapper_paths(paths layout) (string, string) {
	base := filepath.Join(paths.tools, "bootstrap")
	cc, cxx := filepath.Join(base, "hpe-zig-cc"), filepath.Join(base, "hpe-zig-cxx")
	if runtime.GOOS == "windows" {
		cc += ".exe"
		cxx += ".exe"
	}
	return cc, cxx
}

// bundle_linux_runtime implements the bundle linux runtime operation.
func bundle_linux_runtime(zig, install_root string) error {
	lib := filepath.Join(install_root, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"libstdc++.so.6", "libgcc_s.so.1"} {
		command := exec.Command(zig, "c++", "-print-file-name="+name)
		output, err := command.Output()
		if err != nil {
			return err
		}
		source := strings.TrimSpace(string(output))
		if !filepath.IsAbs(source) {
			return fmt.Errorf("could not locate bundled runtime %s: %s", name, source)
		}
		if err := copy_file(source, filepath.Join(lib, name), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// publish_gui implements the publish gui operation.
func publish_gui(paths layout, dotnet, install_root, rid, jobs string) error {
	gui_out := filepath.Join(install_root, "gui")
	if err := os.MkdirAll(filepath.Join(paths.build, "nuget-packages"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(paths.build, "nuget-http-cache"), 0o755); err != nil {
		return err
	}
	env := map[string]string{"NUGET_PACKAGES": filepath.Join(paths.build, "nuget-packages"),
		"NUGET_HTTP_CACHE_PATH":       filepath.Join(paths.build, "nuget-http-cache"),
		"DOTNET_CLI_TELEMETRY_OPTOUT": "1", "DOTNET_SKIP_FIRST_TIME_EXPERIENCE": "1"}
	project := filepath.Join(paths.root, "apps", "gui", "Hpe.Gui", "Hpe.Gui.csproj")
	args := []string{"publish", project, "-c", "Release", "-r", rid, "--self-contained", "true",
		"-m:" + jobs, "-p:PublishSingleFile=false", "-p:PublishTrimmed=false", "-o", gui_out}
	if err := run_command(paths.root, env, dotnet, args...); err != nil {
		return err
	}
	fontdir := filepath.Join(gui_out, "fonts")
	if err := os.RemoveAll(fontdir); err != nil {
		return err
	}
	return nil
}

// write_linux_launcher implements the write linux launcher operation.
func write_linux_launcher(install_root string) error {
	path := filepath.Join(install_root, "hpe-gui")
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return copy_file(executable, path, 0o755)
}

// run_linux_gui_launcher implements the run linux gui launcher operation.
func run_linux_gui_launcher(args []string) int {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := filepath.Dir(executable)
	library_path := filepath.Join(root, "lib") + string(os.PathListSeparator) + filepath.Join(root, "gui")
	if inherited := os.Getenv("LD_LIBRARY_PATH"); inherited != "" {
		library_path += string(os.PathListSeparator) + inherited
	}
	if err := os.Setenv("LD_LIBRARY_PATH", library_path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	user_id := "default"
	if current_user, err := user.Current(); err == nil && current_user.Uid != "" {
		user_id = current_user.Uid
	}
	cache_dir := filepath.Join(os.TempDir(), "hpe-fontconfig-cache-"+user_id)
	if err := os.MkdirAll(cache_dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var escaped_root bytes.Buffer
	if err := xml.EscapeText(&escaped_root, []byte(filepath.Join(root, "lib", "fonts"))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var escaped_cache bytes.Buffer
	if err := xml.EscapeText(&escaped_cache, []byte(cache_dir)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	font_config := fmt.Sprintf("<fontconfig><dir>%s</dir><cachedir>%s</cachedir><alias><family>sans-serif</family><prefer><family>DejaVu Sans</family><family>Noto Sans SC</family></prefer></alias><alias><family>monospace</family><prefer><family>DejaVu Sans</family></prefer></alias></fontconfig>\n", escaped_root.String(), escaped_cache.String())
	config_file, err := os.CreateTemp(os.TempDir(), "hpe-fontconfig-*.conf")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	config_path := config_file.Name()
	defer os.Remove(config_path)
	if _, err := config_file.WriteString(font_config); err != nil {
		_ = config_file.Close()
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := config_file.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.Setenv("FONTCONFIG_FILE", config_path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	gui := filepath.Join(root, "gui", "hpe-gui")
	command := exec.Command(gui, args...)
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

// run_command implements the run command operation.
func run_command(directory string, overrides map[string]string, name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Dir = directory
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	env := make(map[string]string)
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	for key, value := range overrides {
		env[key] = value
	}
	command.Env = make([]string, 0, len(env))
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	started := time.Now()
	fmt.Printf("\n>>> [cwd=%q] %s\n", directory, format_command(name, args))
	env_keys := make([]string, 0, len(overrides))
	for key := range overrides {
		env_keys = append(env_keys, key)
	}
	sort.Strings(env_keys)
	for _, key := range env_keys {
		fmt.Printf("    env %s=%q\n", key, overrides[key])
	}
	if err := command.Run(); err != nil {
		fmt.Printf("<<< FAILED after %s: %v\n", time.Since(started).Round(time.Millisecond), err)
		return fmt.Errorf("command %s failed: %w", name, err)
	}
	fmt.Printf("<<< OK after %s: %s\n", time.Since(started).Round(time.Millisecond), name)
	return nil
}

// executable_exists implements the executable exists operation.
func executable_exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// copy_file implements the copy file operation.
func copy_file(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copy_err := io.Copy(output, input)
	close_err := output.Close()
	if copy_err != nil {
		return copy_err
	}
	if close_err != nil {
		return close_err
	}
	return os.Chmod(destination, mode)
}

// copy_directory implements the copy directory operation.
func copy_directory(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		return copy_file(path, target, mode)
	})
}

// project_layout implements the project layout operation.
func project_layout() (layout, error) {
	root, err := os.Getwd()
	if err != nil {
		return layout{}, err
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "CMakeLists.txt")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			return layout{}, errors.New("run from inside the HPE checkout")
		}
		root = parent
	}
	build := env_path("HPE_BUILD_ROOT", filepath.Join(root, "build"))
	toolchain := env_path("HPE_TOOL_ROOT", filepath.Join(build, "toolchains"))
	return layout{root, build, filepath.Join(build, "tools"), filepath.Join(build, "downloads"), toolchain}, nil
}

// env_path implements the env path operation.
func env_path(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		if absolute, err := filepath.Abs(value); err == nil {
			return absolute
		}
	}
	return fallback
}

// pinned_artifacts implements the pinned artifacts operation.
func pinned_artifacts(p layout) ([]artifact, error) {
	arch := runtime.GOARCH
	zig_os, cmake_os, ninja_name, zig_hash, cmake_hash, ninja_hash := "", "", "", "", "", ""
	var python_arch, python_hash string
	switch runtime.GOOS {
	case "linux":
		zig_os, cmake_os = "linux", "linux"
		switch arch {
		case "amd64":
			zig_hash = "1cbe9df9f27e6b78d14ccbca43b6703a404ef79ef1c463de901d7f088d4e2026"
			cmake_hash = "e5bb807f7728cb60cd8b27ebc97a2edb469b68655f21e844a600c3575b76f5bb"
			ninja_name, ninja_hash = "ninja-linux.zip", "0830252db77884957a1a4b87b05a1e2d9b5f658b8367f82999a941884cbe0238"
			python_arch, python_hash = "x86_64", "c0ccda275948c79996e46993c2c5476ff5cca606dee530f1dea712179131b348"
		case "arm64":
			zig_hash = "9e8d11661d4ae3bd57702a3832781e23ad151dde5798e16a5ccd503f65234ff8"
			cmake_hash = "a1b6cc63636a0e55c63257cf3315a8a5f129e42fade25db1afea4ff8ab06f25e"
			ninja_name, ninja_hash = "ninja-linux-aarch64.zip", "740f1b9f9d8ae68438240a6a2f3f7a27fc8b1946d2024a6a6b25857ee877987b"
			python_arch, python_hash = "aarch64", "baa3d107d17e4328448e30c3c9c83cca0eb41ca7a37c10982e14d46a5c3db07d"
		default:
			return nil, fmt.Errorf("unsupported Linux architecture %s", arch)
		}
	case "windows":
		zig_os, cmake_os = "windows", "windows"
		switch arch {
		case "amd64":
			zig_hash = "b5663f69581dcf391293fbf16c06cb80d81d806545ce618b4d0bab7f0eb8c428"
			cmake_hash = "bace36e94b31c68ab6fa295f26dfa11219e0701cf7c94b0284a7d1cb13dac536"
			ninja_name, ninja_hash = "ninja-win.zip", "26a40fa8595694dec2fad4911e62d29e10525d2133c9a4230b66397774ae25bf"
		case "arm64":
			zig_hash = "0a59d91fa1cb40cf068e9b0954434ce973500c7a2ea749f1e01af62cdab52d26"
			cmake_hash = "ed673bbc4eb7c1e59b0407fd52f1680fc0ae3229a46b9b7b3ea9133764f07f67"
			ninja_name, ninja_hash = "ninja-winarm64.zip", "fb959b674970e36a7c9a23191524b80fb5298fc71fc98bfa42456bcc0a8dfb2f"
		default:
			return nil, fmt.Errorf("unsupported Windows architecture %s", arch)
		}
	default:
		return nil, fmt.Errorf("unsupported host operating system %s", runtime.GOOS)
	}
	zig_file := fmt.Sprintf("zig-%s-%s-%s.zip", arch_name(arch), zig_os, zig_version)
	zig_format, zig_strip := "zip", 1
	if runtime.GOOS == "linux" {
		zig_file = fmt.Sprintf("zig-%s-%s-%s.tar.xz", arch_name(arch), zig_os, zig_version)
		zig_format, zig_strip = "tar.xz", 1
	}
	cmake_arch := arch_name(arch)
	if runtime.GOOS == "windows" && arch == "arm64" {
		cmake_arch = "arm64"
	}
	cmake_file := fmt.Sprintf("cmake-%s-%s-%s.tar.gz", cmake_version, cmake_os, cmake_arch)
	cmake_format, cmake_strip := "tar.gz", 1
	if runtime.GOOS == "windows" {
		cmake_file = fmt.Sprintf("cmake-%s-windows-%s.zip", cmake_version, cmake_arch)
		cmake_format, cmake_strip = "zip", 1
	}
	items := []artifact{
		{name: zig_file, url: "https://ziglang.org/download/" + zig_version + "/" + zig_file, sha256: zig_hash, destination: filepath.Join(p.toolchain, "zig"), format: zig_format, strip: zig_strip},
		{name: cmake_file, url: "https://cmake.org/files/v4.4/" + cmake_file, sha256: cmake_hash, destination: filepath.Join(p.tools, "cmake"), format: cmake_format, strip: cmake_strip},
		{name: ninja_name, url: "https://github.com/ninja-build/ninja/releases/download/v" + ninja_version + "/" + ninja_name, sha256: ninja_hash, destination: filepath.Join(p.tools, "ninja"), format: "zip"},
	}
	if runtime.GOOS == "linux" {
		python_file := fmt.Sprintf("cpython-%s+20251014-%s-unknown-linux-gnu-install_only_stripped.tar.gz", python_version, python_arch)
		items = append(items, artifact{name: python_file, url: "https://github.com/astral-sh/python-build-standalone/releases/download/20251014/" + python_file, sha256: python_hash, destination: filepath.Join(p.tools, "python"), format: "tar.gz", strip: 1})
	}
	dotnet, err := dotnet_artifact(p)
	if err != nil {
		return nil, err
	}
	items = append(items, dotnet)
	for index := range items {
		if items[index].sha256 == "" {
			if artifact_installed(items[index]) {
				continue
			}
			checksum, err := official_cmake_checksum(items[index].name)
			if err != nil {
				return nil, err
			}
			items[index].sha256 = checksum
		}
	}
	return items, nil
}

// arch_name implements the arch name operation.
func arch_name(arch string) string {
	if arch == "amd64" {
		return "x86_64"
	}
	if arch == "arm64" {
		return "aarch64"
	}
	return arch
}

// dotnet_artifact implements the dotnet artifact operation.
func dotnet_artifact(p layout) (artifact, error) {
	destination := filepath.Join(p.toolchain, "dotnet")
	dotnet_binary := filepath.Join(destination, "dotnet")
	if runtime.GOOS == "windows" {
		dotnet_binary += ".exe"
	}
	if digest, ok := installed_artifact_digest(destination, dotnet_binary); ok {
		return artifact{name: "dotnet-sdk-" + dotnet_version, sha256: digest, destination: destination}, nil
	}
	url := "https://builds.dotnet.microsoft.com/dotnet/release-metadata/10.0/releases.json"
	data, err := get_bytes(url)
	if err != nil {
		return artifact{}, err
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(data, &metadata); err != nil {
		return artifact{}, fmt.Errorf("parse .NET release metadata: %w", err)
	}
	var releases []map[string]json.RawMessage
	if err := json.Unmarshal(metadata["releases"], &releases); err != nil {
		return artifact{}, fmt.Errorf("parse .NET release list: %w", err)
	}
	rid_arch := "x64"
	if runtime.GOARCH == "arm64" {
		rid_arch = "arm64"
	}
	rid := runtime.GOOS + "-" + rid_arch
	for _, release := range releases {
		var sdks []map[string]json.RawMessage
		if err := json.Unmarshal(release["sdks"], &sdks); err != nil {
			return artifact{}, fmt.Errorf("parse .NET SDK list: %w", err)
		}
		for _, sdk := range sdks {
			var sdk_version string
			if err := json.Unmarshal(sdk["version"], &sdk_version); err != nil {
				return artifact{}, fmt.Errorf("parse .NET SDK version: %w", err)
			}
			if sdk_version != dotnet_version {
				continue
			}
			var files []map[string]json.RawMessage
			if err := json.Unmarshal(sdk["files"], &files); err != nil {
				return artifact{}, fmt.Errorf("parse .NET SDK files: %w", err)
			}
			for _, file := range files {
				var file_rid, file_name, file_url, file_hash string
				for key, target := range map[string]*string{
					"rid": &file_rid, "name": &file_name, "url": &file_url, "hash": &file_hash,
				} {
					if err := json.Unmarshal(file[key], target); err != nil {
						return artifact{}, fmt.Errorf("parse .NET SDK file %s: %w", key, err)
					}
				}
				if file_rid == rid && (file_name == "sdk" || strings.HasPrefix(file_name, "sdk-") || strings.HasPrefix(file_name, "dotnet-sdk-")) {
					format := "tar.gz"
					if strings.HasSuffix(file_url, ".zip") {
						format = "zip"
					}
					return artifact{name: filepath.Base(file_url), url: file_url, sha256: strings.ToLower(file_hash), destination: filepath.Join(p.toolchain, "dotnet"), format: format}, nil
				}
			}
		}
	}
	return artifact{}, fmt.Errorf(".NET SDK %s archive for %s was not found in official metadata", dotnet_version, rid)
}

// installed_artifact_digest implements the installed artifact digest operation.
func installed_artifact_digest(destination, candidate string) (string, bool) {
	if info, err := os.Stat(candidate); err != nil || info.IsDir() {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(destination, ".hpe-artifact-sha256"))
	if err != nil {
		return "", false
	}
	digest := strings.ToLower(strings.TrimSpace(string(data)))
	if len(digest) != 64 && len(digest) != 128 {
		return "", false
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", false
	}
	return digest, true
}

// official_cmake_checksum implements the official cmake checksum operation.
func official_cmake_checksum(filename string) (string, error) {
	data, err := get_bytes("https://cmake.org/files/v4.4/cmake-" + cmake_version + "-SHA-256.txt")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == filename && len(fields[0]) == 64 {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("official CMake SHA-256 entry for %s is missing", filename)
}

// install_artifact implements the install artifact operation.
func install_artifact(item artifact, downloads string) error {
	if artifact_installed(item) {
		fmt.Printf("Reusing complete installed artifact %s (verified SHA-256 %s)\n", item.name, item.sha256)
		return nil
	}
	if item.sha256 == "" {
		return fmt.Errorf("refusing to use or install %s without a pinned SHA-256", item.name)
	}
	archive := filepath.Join(downloads, item.name)
	if err := download_verified(item.url, archive, item.sha256); err != nil {
		return err
	}
	parent := filepath.Dir(item.destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp := item.destination + ".staging"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	fmt.Printf("Extracting verified archive %s into staging directory %s\n", archive, tmp)
	if err := extract_archive(archive, tmp, item.format, item.strip); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".hpe-artifact-sha256"), []byte(strings.ToLower(item.sha256)+"\n"), 0o644); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := replace_directory(tmp, item.destination); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	fmt.Printf("Installed %s from a SHA-256 verified archive\n", item.name)
	return nil
}

// artifact_installed implements the artifact installed operation.
func artifact_installed(item artifact) bool {
	if item.sha256 == "" {
		return false
	}
	var candidate string
	switch {
	case strings.Contains(filepath.Base(item.destination), "zig"):
		candidate = filepath.Join(item.destination, "zig")
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
	case strings.Contains(filepath.Base(item.destination), "cmake"):
		candidate = filepath.Join(item.destination, "bin", "cmake")
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
	case strings.Contains(filepath.Base(item.destination), "ninja"):
		candidate = filepath.Join(item.destination, "ninja")
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
	case strings.Contains(filepath.Base(item.destination), "python"):
		candidate = filepath.Join(item.destination, "bin", "python3.13")
	case strings.Contains(filepath.Base(item.destination), "dotnet"):
		candidate = filepath.Join(item.destination, "dotnet")
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
	}
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		return false
	}
	data, err := os.ReadFile(filepath.Join(item.destination, ".hpe-artifact-sha256"))
	return err == nil && strings.EqualFold(strings.TrimSpace(string(data)), item.sha256)
}

// download_verified implements the download verified operation.
func download_verified(url, destination, expected string) error {
	if expected == "" {
		return errors.New("refusing to install an archive without a SHA-256")
	}
	info, stat_err := os.Stat(destination)
	if stat_err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		if err := verify_digest(destination, expected); err == nil {
			fmt.Printf("Cached archive verified; skipping download: %s (SHA-256 %s)\n", filepath.Base(destination), expected)
			return nil
		}
		fmt.Printf("Cached archive failed SHA-256 verification; downloading again: %s\n", filepath.Base(destination))
		if err := os.Remove(destination); err != nil {
			return fmt.Errorf("remove invalid cached archive %s: %w", destination, err)
		}
	} else {
		if stat_err != nil && !errors.Is(stat_err, os.ErrNotExist) {
			return stat_err
		}
		if stat_err == nil {
			if err := os.Remove(destination); err != nil {
				return fmt.Errorf("remove incomplete cached archive %s: %w", destination, err)
			}
		}
		fmt.Printf("No complete cached archive; downloading %s\n", filepath.Base(destination))
	}
	if strings.Contains(url, "cmake.org") && expected == "FETCH_OFFICIAL_CHECKSUM" {
		return errors.New("internal checksum lookup should be resolved before download")
	}
	tmp := destination + ".part"
	_ = os.Remove(tmp)
	var last error
	started := time.Now()
	for attempt := 1; attempt <= 5; attempt++ {
		fmt.Printf("Download attempt %d/5: %s\n", attempt, url)
		if err := download_once(url, tmp); err != nil {
			last = err
			fmt.Printf("Download attempt %d failed: %v\n", attempt, err)
			_ = os.Remove(tmp)
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}
		if err := verify_digest(tmp, expected); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if info, err := os.Stat(tmp); err == nil {
			fmt.Printf("Downloaded archive passed SHA-256 verification: %s (%d bytes, %s)\n", filepath.Base(destination), info.Size(), time.Since(started).Round(time.Millisecond))
		}
		if err := os.Rename(tmp, destination); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return nil
	}
	return fmt.Errorf("download failed after retries: %w", last)
}

// download_once implements the download once operation.
func download_once(url, destination string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, response.Status)
	}
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	_, copy_err := io.Copy(file, response.Body)
	close_err := file.Close()
	if copy_err != nil {
		return copy_err
	}
	return close_err
}

// get_bytes implements the get bytes operation.
func get_bytes(url string) ([]byte, error) {
	fmt.Printf("Fetching release/checksum metadata: %s\n", url)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, response.Status)
	}
	data, err := io.ReadAll(response.Body)
	if err == nil {
		fmt.Printf("Fetched metadata (%d bytes): %s\n", len(data), url)
	}
	return data, err
}

// verify_digest implements the verify digest operation.
func verify_digest(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if len(expected) == 128 {
		hash = sha512.New()
	}
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("SHA-256 mismatch for %s: expected %s, received %s", filepath.Base(path), expected, actual)
	}
	return nil
}

// extract_archive implements the extract archive operation.
func extract_archive(archive, destination, format string, strip int) error {
	switch format {
	case "zip":
		return extract_zip(archive, destination, strip)
	case "tar.gz":
		return extract_tar_gz(archive, destination, strip)
	case "tar.xz":
		return extract_tar_xz(archive, destination, strip)
	default:
		return fmt.Errorf("unsupported archive format %q", format)
	}
}

// extract_zip implements the extract zip operation.
func extract_zip(archive, destination string, strip int) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		name, skip, err := safe_archive_path(file.Name, strip)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		target := filepath.Join(destination, name)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		source, err := file.Open()
		if err != nil {
			return err
		}
		mode := file.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		if runtime.GOOS != "windows" && mode&0o111 != 0 {
			mode |= 0o111
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			_ = source.Close()
			return err
		}
		_, copy_err := io.Copy(output, source)
		close_err := output.Close()
		_ = source.Close()
		if copy_err != nil {
			return copy_err
		}
		if close_err != nil {
			return close_err
		}
	}
	return nil
}

// extract_tar_gz implements the extract tar gz operation.
func extract_tar_gz(archive, destination string, strip int) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	return extract_tar(gz, destination, strip)
}

// extract_tar_xz implements the extract tar xz operation.
func extract_tar_xz(archive, destination string, strip int) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := xz.NewReader(file)
	if err != nil {
		return err
	}
	return extract_tar(reader, destination, strip)
}

// extract_tar implements the extract tar operation.
func extract_tar(source io.Reader, destination string, strip int) error {
	reader := tar.NewReader(source)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name, skip, err := safe_archive_path(header.Name, strip)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		target := filepath.Join(destination, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)&0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
			_, copy_err := io.Copy(output, reader)
			close_err := output.Close()
			if copy_err != nil {
				return copy_err
			}
			if close_err != nil {
				return close_err
			}
			if !header.ModTime.IsZero() {
				if err := os.Chtimes(target, header.ModTime, header.ModTime); err != nil {
					return err
				}
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			link_target := filepath.Clean(filepath.Join(filepath.Dir(target), filepath.FromSlash(header.Linkname)))
			base, err := filepath.Abs(destination)
			if err != nil {
				return err
			}
			resolved, err := filepath.Abs(link_target)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(base, resolved)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return fmt.Errorf("tar symlink escapes extraction directory: %s -> %s", header.Name, header.Linkname)
			}
			if err := os.Symlink(filepath.FromSlash(header.Linkname), target); err != nil {
				return err
			}
		case tar.TypeLink:
			link_name, skip, err := safe_archive_path(header.Linkname, strip)
			if err != nil {
				return err
			}
			if skip {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Link(filepath.Join(destination, link_name), target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported tar entry type %d in %s", header.Typeflag, header.Name)
		}
	}
}

// safe_archive_path implements the safe archive path operation.
func safe_archive_path(name string, strip int) (string, bool, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	parts := strings.Split(strings.Trim(name, "/"), "/")
	if filepath.IsAbs(name) || len(parts) <= strip {
		return "", true, nil
	}
	parts = parts[strip:]
	clean := filepath.Clean(filepath.Join(parts...))
	if clean == "." {
		return "", true, nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("archive entry escapes extraction directory: %s", name)
	}
	return clean, false, nil
}

// replace_directory implements the replace directory operation.
func replace_directory(staged, destination string) error {
	old := destination + ".previous"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(destination); err == nil {
		if err := os.Rename(destination, old); err != nil {
			return err
		}
	}
	if err := os.Rename(staged, destination); err != nil {
		_ = os.Rename(old, destination)
		return err
	}
	return os.RemoveAll(old)
}
