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
	zigVersion    = "0.17.0"
	cmakeVersion  = "4.4.4"
	ninjaVersion  = "1.13.1"
	pythonVersion = "3.13.9"
	goVersion     = "1.27.2"
	dotnetVersion = "10.0.100"
	maxBuildLogs  = 10
)

type layout struct {
	root, build, tools, downloads, toolchain string
}

type artifact struct {
	name, url, sha256, destination, format string
	strip                                  int
}

func main() {
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(os.Args[0]), filepath.Ext(os.Args[0])))
	if name == "hpe-zig-cc" || name == "hpe-zig-cxx" {
		os.Exit(runZigWrapper(name == "hpe-zig-cxx", os.Args[1:]))
	}
	if name == "git" {
		os.Exit(runGitVersionShim(os.Args[1:]))
	}
	if name == "hpe-gui" && runtime.GOOS == "linux" {
		os.Exit(runLinuxGuiLauncher(os.Args[1:]))
	}
	if strings.HasPrefix(name, "hpe-zig-") {
		os.Exit(runZigTool(strings.TrimPrefix(name, "hpe-zig-"), os.Args[1:]))
	}
	os.Exit(runBootstrap(os.Args[1:]))
}

func runBootstrap(args []string) int {
	var capture *buildLog
	commandName := ""
	if len(args) > 0 {
		switch args[0] {
		case "tools", "build", "clean", "distclean":
			commandName = args[0]
		}
	}
	if commandName != "" {
		paths, err := projectLayout()
		if err != nil {
			fmt.Fprintln(os.Stderr, "hpe-bootstrap:", err)
			return 1
		}
		if err := prepareManagedRoots(paths); err != nil {
			fmt.Fprintln(os.Stderr, "hpe-bootstrap:", err)
			return 1
		}
		logDir := buildLogDirectory(paths)
		capture, err = startBuildLog(logDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "hpe-bootstrap: cannot create build log:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "Build log: %s\n", capture.path)
		fmt.Fprintf(os.Stdout, "HPE %s started at %s (%s/%s)\n", commandName, time.Now().Format(time.RFC3339), runtime.GOOS, runtime.GOARCH)
		fmt.Fprintf(os.Stdout, "Working directory: %q\nProject root: %q\nBuild root: %q\nToolchain root: %q\nCommand: %s\n\n",
			currentDirectory(), paths.root, paths.build, paths.toolchain, formatCommand("hpe-bootstrap", args))
		if err := pruneBuildLogs(filepath.Dir(capture.path), maxBuildLogs); err != nil {
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
		fmt.Fprintf(os.Stdout, "\nHPE %s %s at %s\nBuild log saved to: %s\n", commandName, status, time.Now().Format(time.RFC3339), capture.path)
		if closeErr := capture.Close(); closeErr != nil {
			fmt.Fprintf(capture.consoleErr, "hpe-bootstrap: finalize build log: %v\n", closeErr)
			code = 1
		}
	}
	return code
}

func prepareManagedRoots(paths layout) error {
	rootBuild := filepath.Join(paths.root, "build")
	rootToolchain := filepath.Join(paths.root, "toolchain")
	rootInstall := filepath.Join(paths.root, "install")
	defaultInstall := filepath.Join(rootInstall, hostRuntimeID())
	customInstall := envPath("HPE_INSTALL_ROOT", defaultInstall)
	managedRoots := []string{paths.build, paths.toolchain, rootInstall}
	if !samePath(customInstall, rootInstall) && !pathContains(rootInstall, customInstall) {
		managedRoots = append(managedRoots, customInstall)
	}
	if err := validateDisjointGeneratedPaths(managedRoots...); err != nil {
		return err
	}
	buildEntries := []string{"logs", "downloads", "tools", "projects", "ccache-build", "ccache-source", "ccache-cache", "vcpkg-root", "vcpkg-downloads", "host-tools", "manifests", "nuget-packages", "nuget-http-cache"}
	if logEntry, inBuildRoot, err := buildLogRootEntry(paths); err != nil {
		return err
	} else if inBuildRoot && logEntry != "." {
		buildEntries = append(buildEntries, logEntry)
	}
	if err := prepareManagedRoot(paths.root, paths.build, rootBuild, ".hpe-build-root", buildEntries); err != nil {
		return err
	}
	if err := prepareManagedRoot(paths.root, paths.toolchain, rootToolchain, ".hpe-toolchain-root", []string{"go", "zig", "dotnet"}); err != nil {
		return err
	}
	if samePath(customInstall, defaultInstall) || samePath(customInstall, rootInstall) || pathContains(rootInstall, customInstall) {
		return nil
	}
	return prepareManagedRoot(paths.root, customInstall, defaultInstall, ".hpe-install-root",
		[]string{"gui", "lib", "examples", "hpe-cli", "hpe-cli.exe", "hpe-gui", "hpe-gui.cmd", "hpe_core.dll", "hpe_gui_bridge.dll"})
}

func buildLogDirectory(paths layout) string {
	return envPath("HPE_LOG_DIR", filepath.Join(paths.build, "logs"))
}

func buildLogRootEntry(paths layout) (string, bool, error) {
	logDir, err := filepath.Abs(buildLogDirectory(paths))
	if err != nil {
		return "", false, err
	}
	buildRoot, err := filepath.Abs(paths.build)
	if err != nil {
		return "", false, err
	}
	relative, err := filepath.Rel(buildRoot, logDir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", false, nil
	}
	if relative == "." {
		return ".", true, nil
	}
	return strings.Split(relative, string(os.PathSeparator))[0], true, nil
}

func prepareManagedRoot(projectRoot, directory, defaultDirectory, markerName string, allowedEntries []string) error {
	path, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to manage symlink project root %s", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if samePath(path, defaultDirectory) {
		return nil
	}
	if err := validateGeneratedPath(projectRoot, path); err != nil {
		return fmt.Errorf("unsafe custom project root: %w", err)
	}
	marker := filepath.Join(path, markerName)
	expected := filepath.Clean(path) + "\nproject=" + filepath.Clean(projectRoot) + "\n"
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
	allowed := make(map[string]bool, len(allowedEntries))
	for _, entry := range allowedEntries {
		allowed[entry] = true
	}
	for _, entry := range entries {
		if markerName == ".hpe-build-root" && strings.HasPrefix(entry.Name(), "build-") && strings.HasSuffix(entry.Name(), ".log") {
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

func pruneBuildLogs(directory string, keep int) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	type logEntry struct {
		name    string
		modTime time.Time
	}
	logs := make([]logEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "build-") || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %s: %w", entry.Name(), err)
		}
		if info.Mode().IsRegular() {
			logs = append(logs, logEntry{name: entry.Name(), modTime: info.ModTime()})
		}
	}
	sort.Slice(logs, func(i, j int) bool {
		if logs[i].modTime.Equal(logs[j].modTime) {
			return logs[i].name < logs[j].name
		}
		return logs[i].modTime.Before(logs[j].modTime)
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

type buildLog struct {
	path       string
	file       *os.File
	consoleOut *os.File
	consoleErr *os.File
	stdoutPipe *os.File
	stderrPipe *os.File
	drains     sync.WaitGroup
}

type synchronizedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *synchronizedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(data)
}

func startBuildLog(directory string) (*buildLog, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(directory, "build-"+time.Now().Format("20060102-150405")+"-*.log")
	if err != nil {
		return nil, err
	}
	stdoutRead, stdoutPipe, err := os.Pipe()
	if err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	stderrRead, stderrPipe, err := os.Pipe()
	if err != nil {
		stdoutRead.Close()
		stdoutPipe.Close()
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	log := &buildLog{
		path: filepath.Clean(file.Name()), file: file,
		consoleOut: os.Stdout, consoleErr: os.Stderr,
		stdoutPipe: stdoutPipe, stderrPipe: stderrPipe,
	}
	sharedLog := &synchronizedWriter{w: file}
	log.drains.Add(2)
	go func() {
		defer log.drains.Done()
		_, _ = io.Copy(io.MultiWriter(log.consoleOut, sharedLog), stdoutRead)
		_ = stdoutRead.Close()
	}()
	go func() {
		defer log.drains.Done()
		_, _ = io.Copy(io.MultiWriter(log.consoleErr, sharedLog), stderrRead)
		_ = stderrRead.Close()
	}()
	os.Stdout, os.Stderr = stdoutPipe, stderrPipe
	return log, nil
}

func (log *buildLog) Close() error {
	_ = log.stdoutPipe.Close()
	_ = log.stderrPipe.Close()
	log.drains.Wait()
	os.Stdout, os.Stderr = log.consoleOut, log.consoleErr
	syncErr := log.file.Sync()
	closeErr := log.file.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func currentDirectory() string {
	directory, err := os.Getwd()
	if err != nil {
		return "unknown"
	}
	return directory
}

func formatCommand(name string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, strconv.Quote(name))
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

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
	paths, err := projectLayout()
	if err != nil {
		return err
	}
	switch args[0] {
	case "build":
		return buildProject(paths)
	case "clean":
		return cleanProject(paths)
	case "distclean":
		return distcleanProject(paths)
	default:
		return runStep("Prepare compiler SDKs and local build tools", func() error { return ensureTools(paths) })
	}
}

func runStep(name string, action func() error) error {
	started := time.Now()
	fmt.Printf("\n=== %s ===\n", name)
	if err := action(); err != nil {
		fmt.Printf("=== FAILED: %s (%s): %v ===\n", name, time.Since(started).Round(time.Millisecond), err)
		return fmt.Errorf("%s: %w", name, err)
	}
	fmt.Printf("=== DONE: %s (%s) ===\n", name, time.Since(started).Round(time.Millisecond))
	return nil
}

func cleanProject(paths layout) error {
	return runStep("Clean intermediate build outputs and install packages", func() error {
		rootInstall := filepath.Join(paths.root, "install")
		customInstall := envPath("HPE_INSTALL_ROOT", rootInstall)
		remove := []string{
			filepath.Join(paths.build, "projects"),
			filepath.Join(paths.build, "ccache-build"),
			filepath.Join(paths.build, "host-tools", "build"),
			filepath.Join(paths.build, "manifests"),
			rootInstall,
		}
		rootDotnetBuild := filepath.Join(paths.root, "build", "dotnet")
		if !samePath(rootDotnetBuild, paths.build) && !pathContains(rootDotnetBuild, paths.build) && !pathContains(paths.build, rootDotnetBuild) {
			remove = append(remove, rootDotnetBuild)
		}
		if !samePath(customInstall, rootInstall) && !pathContains(rootInstall, customInstall) {
			remove = append(remove, customInstall)
		}
		protected := []string{paths.build, paths.toolchain, rootInstall}
		if !samePath(customInstall, rootInstall) && !pathContains(rootInstall, customInstall) {
			protected = append(protected, customInstall)
		}
		if !samePath(rootDotnetBuild, paths.build) && !pathContains(rootDotnetBuild, paths.build) && !pathContains(paths.build, rootDotnetBuild) {
			protected = append(protected, rootDotnetBuild)
		}
		if err := validateDisjointGeneratedPaths(protected...); err != nil {
			return err
		}
		for _, target := range remove {
			if err := validateGeneratedPath(paths.root, target); err != nil {
				return err
			}
		}
		for _, target := range remove {
			if err := removeGeneratedPath(paths.root, target); err != nil {
				return err
			}
		}
		fmt.Printf("Kept downloaded archives, compiler SDKs, dependency sources, installed build tools, ccache, package caches, and build logs.\n")
		return nil
	})
}

func distcleanProject(paths layout) error {
	return runStep("Remove downloaded toolchains, dependencies, intermediate outputs, and installed programs", func() error {
		rootInstall := filepath.Join(paths.root, "install")
		customInstall := envPath("HPE_INSTALL_ROOT", rootInstall)
		remove := []string{paths.toolchain, rootInstall}
		if !samePath(customInstall, rootInstall) && !pathContains(rootInstall, customInstall) {
			remove = append(remove, customInstall)
		}
		protected := []string{paths.build, paths.toolchain, rootInstall}
		if !samePath(customInstall, rootInstall) && !pathContains(rootInstall, customInstall) {
			protected = append(protected, customInstall)
		}
		rootDotnetBuild := filepath.Join(paths.root, "build", "dotnet")
		if !samePath(rootDotnetBuild, paths.build) && !pathContains(rootDotnetBuild, paths.build) && !pathContains(paths.build, rootDotnetBuild) {
			protected = append(protected, rootDotnetBuild)
		}
		if err := validateDisjointGeneratedPaths(protected...); err != nil {
			return err
		}
		if err := validateGeneratedPath(paths.root, paths.build); err != nil {
			return err
		}
		for _, target := range remove {
			if err := validateGeneratedPath(paths.root, target); err != nil {
				return err
			}
		}
		rootDotnetBuild = filepath.Join(paths.root, "build", "dotnet")
		if !samePath(rootDotnetBuild, paths.build) && !pathContains(rootDotnetBuild, paths.build) {
			if err := removeGeneratedPath(paths.root, rootDotnetBuild); err != nil {
				return err
			}
		}
		for _, target := range []string{filepath.Join(paths.root, "out"), filepath.Join(paths.root, ".ccache")} {
			managed := false
			for _, root := range append(append([]string(nil), protected...), customInstall) {
				if pathsOverlap(target, root) {
					managed = true
					break
				}
			}
			if !managed {
				if err := removeGeneratedPath(paths.root, target); err != nil {
					return err
				}
			}
		}
		for _, target := range remove {
			if err := removeGeneratedPath(paths.root, target); err != nil {
				return err
			}
		}
		buildInfo, err := os.Stat(paths.build)
		if errors.Is(err, os.ErrNotExist) {
			fmt.Printf("Build root already absent: %s\n", paths.build)
			return nil
		}
		if err != nil {
			return err
		}
		if !buildInfo.IsDir() {
			return fmt.Errorf("build root is not a directory: %s", paths.build)
		}
		if err := validateGeneratedPath(paths.root, paths.build); err != nil {
			return err
		}
		entries, err := os.ReadDir(paths.build)
		if err != nil {
			return err
		}
		logEntry, logDirInBuildRoot, err := buildLogRootEntry(paths)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if (entry.Name() == "logs" && entry.IsDir()) || (logDirInBuildRoot && logEntry != "." && entry.Name() == logEntry && entry.IsDir()) {
				fmt.Printf("Keeping bounded build log history: %s\n", filepath.Join(paths.build, entry.Name()))
				continue
			}
			if logDirInBuildRoot && logEntry == "." && !entry.IsDir() && strings.HasPrefix(entry.Name(), "build-") && strings.HasSuffix(entry.Name(), ".log") {
				fmt.Printf("Keeping bounded build log: %s\n", filepath.Join(paths.build, entry.Name()))
				continue
			}
			if entry.Name() == ".hpe-build-root" {
				fmt.Printf("Keeping custom build-root ownership marker: %s\n", filepath.Join(paths.build, entry.Name()))
				continue
			}
			if err := removeGeneratedPath(paths.root, filepath.Join(paths.build, entry.Name())); err != nil {
				return err
			}
		}
		fmt.Printf("Distclean complete; all tool downloads and generated build data were removed.\n")
		return nil
	})
}

func removeGeneratedPath(projectRoot, target string) error {
	if err := validateGeneratedPath(projectRoot, target); err != nil {
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

func validateGeneratedPath(projectRoot, target string) error {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return err
	}
	path, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	volumeRoot := filepath.VolumeName(path) + string(os.PathSeparator)
	if samePath(path, volumeRoot) || samePath(path, root) || pathContains(path, root) {
		return fmt.Errorf("refusing to remove unsafe generated path %s", path)
	}
	return nil
}

func samePath(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(first, second)
	}
	return first == second
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func validateDisjointGeneratedPaths(paths ...string) error {
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
			if pathsOverlap(left, right) {
				return fmt.Errorf("generated paths overlap and cannot be safely cleaned: %s and %s", left, right)
			}
		}
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	return samePath(first, second) || pathContains(first, second) || pathContains(second, first)
}

func ensureTools(paths layout) error {
	if err := os.MkdirAll(paths.downloads, 0o755); err != nil {
		return err
	}
	var artifacts []artifact
	if err := runStep("Resolve pinned SDK and host tool checksums", func() error {
		var err error
		artifacts, err = pinnedArtifacts(paths)
		return err
	}); err != nil {
		return err
	}
	for _, item := range artifacts {
		if item.destination == "" {
			continue
		}
		if err := runStep("Verify/download/install "+item.name, func() error { return installArtifact(item, paths.downloads) }); err != nil {
			return fmt.Errorf("install %s: %w", item.name, err)
		}
	}
	if runtime.GOOS == "linux" {
		if err := runStep("Prepare project-local compiler and Git compatibility wrappers", func() error { return installZigWrappers(paths) }); err != nil {
			return err
		}
		if err := runStep("Build project-local Linux host tools", func() error { return ensureLinuxHostTools(paths, buildJobs()) }); err != nil {
			return err
		}
	}
	fmt.Printf("Local compiler and build tools are ready under %s and %s\n", paths.toolchain, paths.tools)
	return nil
}

func runZigWrapper(cxx bool, args []string) int {
	zig := os.Getenv("HPE_ZIG")
	if zig == "" {
		fmt.Fprintln(os.Stderr, "HPE_ZIG is not set")
		return 2
	}
	for _, arg := range args {
		if arg == "-E" {
			for _, input := range args {
				if strings.HasSuffix(input, ".gperf.h") {
					return runGperfPreprocessor(zig, args)
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

func runZigTool(tool string, args []string) int {
	if tool == "nm" {
		return runLocalNM(args)
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
		args, err = expandAtFiles(args)
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

func runGitVersionShim(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("git version 2.50.1")
		return 0
	}
	for index, arg := range args {
		if arg != "apply" {
			continue
		}
		var patchPath string
		for _, candidate := range args[index+1:] {
			if !strings.HasPrefix(candidate, "-") {
				patchPath = candidate
				break
			}
		}
		if patchPath == "" {
			fmt.Fprintln(os.Stderr, "hpe git compatibility: git apply requires a patch file")
			return 2
		}
		if err := applyUnifiedPatch(patchPath); err != nil {
			fmt.Fprintln(os.Stderr, "hpe git compatibility:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "hpe bootstrap Git compatibility supports only --version and apply")
	return 1
}

var unifiedHunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

type unifiedHunk struct {
	oldStart int
	newStart int
	lines    []string
}

func applyUnifiedPatch(patchPath string) error {
	patchBytes, err := os.ReadFile(patchPath)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(patchBytes), "\r\n", "\n"), "\n")
	for index := 0; index < len(lines); {
		if !strings.HasPrefix(lines[index], "--- ") {
			index++
			continue
		}
		oldPath := strings.Fields(strings.TrimPrefix(lines[index], "--- "))[0]
		index++
		if index >= len(lines) || !strings.HasPrefix(lines[index], "+++ ") {
			return errors.New("malformed unified patch: missing +++ file header")
		}
		newPath := strings.Fields(strings.TrimPrefix(lines[index], "+++ "))[0]
		index++
		path := newPath
		if path == "/dev/null" {
			path = oldPath
		}
		for _, prefix := range []string{"a/", "b/", "i/", "w/"} {
			path = strings.TrimPrefix(path, prefix)
		}
		target := filepath.Clean(filepath.Join(".", filepath.FromSlash(path)))
		if target == "." || target == ".." || strings.HasPrefix(target, ".."+string(filepath.Separator)) || filepath.IsAbs(target) {
			return fmt.Errorf("patch path escapes the working directory: %s", path)
		}
		var hunks []unifiedHunk
		for index < len(lines) {
			match := unifiedHunkHeader.FindStringSubmatch(lines[index])
			if match == nil {
				if strings.HasPrefix(lines[index], "--- ") || strings.HasPrefix(lines[index], "diff --git ") {
					break
				}
				index++
				continue
			}
			oldStart, _ := strconv.Atoi(match[1])
			newStart, _ := strconv.Atoi(match[3])
			hunk := unifiedHunk{oldStart: oldStart, newStart: newStart}
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
		if err := applyFileHunks(target, oldPath == "/dev/null", newPath == "/dev/null", hunks); err != nil {
			return err
		}
	}
	return nil
}

func applyFileHunks(path string, create, remove bool, hunks []unifiedHunk) error {
	original, err := os.ReadFile(path)
	if err != nil && !create {
		return err
	}
	oldText := strings.ReplaceAll(string(original), "\r\n", "\n")
	oldLines := strings.Split(oldText, "\n")
	oldFinalNewline := strings.HasSuffix(oldText, "\n")
	if len(oldLines) > 0 && oldLines[len(oldLines)-1] == "" && oldFinalNewline {
		oldLines = oldLines[:len(oldLines)-1]
	}
	var updated []string
	cursor := 0
	for _, hunk := range hunks {
		expected := hunk.oldStart - 1
		if hunk.oldStart == 0 {
			expected = 0
		}
		if expected < cursor {
			expected = cursor
		}
		start := -1
		bestDistance := len(oldLines) + 1
		for candidate := cursor; candidate <= len(oldLines); candidate++ {
			if !unifiedHunkMatches(oldLines, candidate, hunk) {
				continue
			}
			distance := candidate - expected
			if distance < 0 {
				distance = -distance
			}
			if distance < bestDistance {
				start, bestDistance = candidate, distance
			}
		}
		if start < 0 {
			return fmt.Errorf("patch hunk does not match %s near line %d", path, expected+1)
		}
		updated = append(updated, oldLines[cursor:start]...)
		position := start
		for _, line := range hunk.lines {
			prefix, text := line[0], line[1:]
			switch prefix {
			case ' ':
				if position >= len(oldLines) || !patchLinesEqual(oldLines[position], text) {
					return fmt.Errorf("patch context does not match %s at line %d", path, position+1)
				}
				updated = append(updated, text)
				position++
			case '-':
				if position >= len(oldLines) || !patchLinesEqual(oldLines[position], text) {
					return fmt.Errorf("patch removal does not match %s at line %d", path, position+1)
				}
				position++
			case '+':
				updated = append(updated, text)
			}
		}
		cursor = position
	}
	updated = append(updated, oldLines[cursor:]...)
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
	if oldFinalNewline || create {
		result += "\n"
	}
	return os.WriteFile(path, []byte(result), mode)
}

func unifiedHunkMatches(source []string, start int, hunk unifiedHunk) bool {
	position := start
	for _, line := range hunk.lines {
		prefix, expected := line[0], line[1:]
		if prefix != ' ' && prefix != '-' {
			continue
		}
		if position >= len(source) || !patchLinesEqual(source[position], expected) {
			return false
		}
		position++
	}
	return true
}

func patchLinesEqual(left, right string) bool {
	return strings.Join(strings.Fields(left), " ") == strings.Join(strings.Fields(right), " ")
}

func expandAtFiles(args []string) ([]string, error) {
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

func runLocalNM(args []string) int {
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

func runGperfPreprocessor(zig string, arguments []string) int {
	sourceIndex := -1
	outputIndex := -1
	for i, arg := range arguments {
		if strings.HasSuffix(arg, ".gperf.h") {
			sourceIndex = i
		}
		if arg == "-o" && i+1 < len(arguments) {
			outputIndex = i + 1
		}
	}
	if sourceIndex < 0 {
		return 2
	}
	source, err := filepath.Abs(arguments[sourceIndex])
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
	cleanSource := filepath.Join(temp, filepath.Base(source))
	if err := os.WriteFile(cleanSource, append(bytes.TrimRight(input, "\n"), '\n'), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	args := append([]string(nil), arguments...)
	args[sourceIndex] = cleanSource
	args = append(args, "-I", filepath.Dir(source))
	cleanOutput := filepath.Join(temp, "preprocessed.gperf")
	if outputIndex >= 0 {
		if out, err := filepath.Abs(arguments[outputIndex]); err == nil {
			cleanOutput = filepath.Join(temp, filepath.Base(out))
			args[outputIndex] = cleanOutput
		}
	}
	args = removeArgument(args, "-c")
	command := exec.Command(zig, append([]string{"cc"}, args...)...)
	var stdout bytes.Buffer
	if outputIndex < 0 {
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
	if outputIndex >= 0 {
		generated, err = os.ReadFile(cleanOutput)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	filtered := filterGperfCutouts(string(generated))
	if !strings.Contains(filtered, "struct FcObjectTypeInfo {") {
		fmt.Fprintln(os.Stderr, "gperf preprocessing did not preserve struct FcObjectTypeInfo")
		return 1
	}
	marker := strings.Index(filtered, "struct FcObjectTypeInfo {")
	if !strings.Contains(filtered[marker:], "%%") {
		fmt.Fprintln(os.Stderr, "gperf preprocessing did not preserve the data separator")
		return 1
	}
	if outputIndex >= 0 {
		out, err := filepath.Abs(arguments[outputIndex])
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

func removeArgument(arguments []string, remove string) []string {
	result := make([]string, 0, len(arguments))
	for _, arg := range arguments {
		if arg != remove {
			result = append(result, arg)
		}
	}
	return result
}

func filterGperfCutouts(source string) string {
	var output strings.Builder
	inCutout := false
	for _, line := range strings.Split(source, "\n") {
		switch strings.TrimSpace(line) {
		case "CUT_OUT_BEGIN":
			inCutout = true
			continue
		case "CUT_OUT_END":
			inCutout = false
			continue
		}
		if !inCutout {
			output.WriteString(line)
			output.WriteByte('\n')
		}
	}
	return output.String()
}

func buildProject(paths layout) error {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return fmt.Errorf("building on %s is not supported", runtime.GOOS)
	}
	if err := runStep("Verify or download and install compiler SDKs and build tools", func() error { return ensureTools(paths) }); err != nil {
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
	if err := runStep("Prepare project-local compiler and Git compatibility wrappers", func() error { return installZigWrappers(paths) }); err != nil {
		return err
	}
	var ccache string
	if err := runStep("Prepare vcpkg dependency source and incremental ccache build", func() error {
		var err error
		ccache, err = prepareCcache(paths, cmake, ninja, zig)
		return err
	}); err != nil {
		return err
	}
	jobs := buildJobs()
	return runStep("Configure, incrementally compile, and install HPE", func() error {
		return configureBuildInstall(paths, cmake, ninja, zig, dotnet, ccache, jobs)
	})
}

func buildJobs() string {
	if value := os.Getenv("HPE_JOBS"); value != "" {
		return value
	}
	return fmt.Sprintf("%d", runtime.NumCPU())
}

func installZigWrappers(paths layout) error {
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
			if err := copyFile(executable, wrapper, 0o755); err != nil {
				return err
			}
			continue
		}
		_ = os.Remove(wrapper)
		if err := os.Symlink(executable, wrapper); err != nil {
			if err := copyFile(executable, wrapper, 0o755); err != nil {
				return err
			}
		}
	}
	return nil
}

func prepareCcache(paths layout, cmake, ninja, zig string) (string, error) {
	vcpkg := filepath.Join(paths.build, "vcpkg-root")
	if err := runStep("Verify or restore pinned vcpkg source", func() error {
		return ensurePinnedSource(paths, "https://codeload.github.com/microsoft/vcpkg/tar.gz/9e593bb18ea69cc5095e012465dcd675a822ed0d",
			"e9255b62719c659f224fcc639b1bf4a6226e4996ad3fb006e18e1533cad77725", vcpkg)
	}); err != nil {
		return "", err
	}
	if err := runStep("Prepare the pinned vcpkg executable", func() error { return bootstrapVcpkg(vcpkg) }); err != nil {
		return "", err
	}
	if err := runStep("Prepare offline filesystem package registry", func() error { return prepareFilesystemRegistry(vcpkg) }); err != nil {
		return "", err
	}
	if runtime.GOOS == "linux" {
		if err := runStep("Configure vcpkg to use the bundled Python runtime", func() error { return patchVcpkgPython(vcpkg) }); err != nil {
			return "", err
		}
	}

	source := filepath.Join(paths.build, "ccache-source")
	if err := runStep("Verify or restore pinned ccache source", func() error {
		return ensurePinnedSource(paths, "https://codeload.github.com/ccache/ccache/tar.gz/ec510e9b0f333b8e97aee98108de3e0858340d59",
			"87d3ecc8dea48ab4e311e4fd24d4b1413caa9bde0c90965e04d9f7660e5b3c9c", source)
	}); err != nil {
		return "", err
	}
	if err := runStep("Apply local ccache portability patch", func() error { return patchCcache(source) }); err != nil {
		return "", err
	}
	buildDir := filepath.Join(paths.build, "ccache-build")
	prefix := filepath.Join(paths.tools, "ccache")
	ccache := filepath.Join(prefix, "bin", "ccache")
	if runtime.GOOS == "windows" {
		ccache += ".exe"
	}
	if executableExists(ccache) {
		if output, err := exec.Command(ccache, "--version").CombinedOutput(); err == nil {
			fmt.Printf("Reusing validated ccache at %s: %s\n", ccache, strings.TrimSpace(string(output)))
			return ccache, nil
		}
		fmt.Printf("Installed ccache failed its version check; rebuilding: %s\n", ccache)
		if err := os.Remove(ccache); err != nil {
			return "", err
		}
	}
	env := compilerEnvironment(paths, zig, "", "")
	cc, cxx := wrapperPaths(paths)
	args := []string{"-S", source, "-B", buildDir, "-G", "Ninja", "-DCMAKE_MAKE_PROGRAM=" + ninja,
		"-DCMAKE_C_COMPILER=" + cc,
		"-DCMAKE_ASM_COMPILER=" + cc,
		"-DCMAKE_CXX_COMPILER=" + cxx,
		"-DCMAKE_POLICY_VERSION_MINIMUM=3.5", "-DZSTD_FROM_INTERNET=ON", "-DHIREDIS_FROM_INTERNET=OFF",
		"-DREDIS_STORAGE_BACKEND=OFF", "-DCCACHE_DEV_MODE=OFF", "-DBUILD_TESTING=OFF",
		"-DCMAKE_BUILD_TYPE=Release", "-DCMAKE_INSTALL_PREFIX=" + prefix}
	if err := runStep("Configure ccache with the bundled Zig toolchain", func() error { return runCommand(paths.root, env, cmake, args...) }); err != nil {
		return "", err
	}
	if err := runStep("Compile ccache", func() error {
		return runCommand(paths.root, env, cmake, "--build", buildDir, "--target", "ccache", "--parallel", buildJobs(), "--verbose")
	}); err != nil {
		return "", err
	}
	if err := runStep("Install ccache into the local build tools", func() error { return runCommand(paths.root, env, cmake, "--install", buildDir) }); err != nil {
		return "", err
	}
	return ccache, nil
}

func ensurePinnedSource(paths layout, url, digest, destination string) error {
	marker := filepath.Join(destination, ".hpe-source-verified")
	if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) == digest {
		fmt.Printf("Reusing verified source tree %s (SHA-256 %s)\n", destination, digest)
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
	archive := filepath.Join(paths.downloads, filepath.Base(destination)+".tar.gz")
	if err := downloadVerified(url, archive, digest); err != nil {
		return err
	}
	if err := extractTarGz(archive, destination, 1); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(digest+"\n"), 0o644)
}

func bootstrapVcpkg(root string) error {
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
	if err := downloadVerified(url, path, digest); err != nil {
		return err
	}
	fmt.Printf("Verified the local vcpkg executable at %s\n", path)
	if runtime.GOOS != "windows" {
		return os.Chmod(path, 0o755)
	}
	return nil
}

func patchCcache(source string) error {
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

func prepareFilesystemRegistry(root string) error {
	baselinePath := filepath.Join(root, "versions", "baseline.json")
	data, err := os.ReadFile(baselinePath)
	if err != nil {
		return err
	}
	var baselines map[string]map[string]struct {
		Baseline    string `json:"baseline"`
		PortVersion int    `json:"port-version"`
	}
	if err := json.Unmarshal(data, &baselines); err != nil {
		return err
	}
	baseline, ok := baselines["default"]
	if !ok {
		return errors.New("vcpkg filesystem registry source is missing the default baseline")
	}
	for name, version := range baseline {
		versionFile := filepath.Join(root, "versions", string(name[0])+"-", name+".json")
		data, err := os.ReadFile(versionFile)
		if err != nil {
			return fmt.Errorf("read vcpkg version file for %s: %w", name, err)
		}
		var record struct {
			Versions []map[string]json.RawMessage `json:"versions"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return fmt.Errorf("parse vcpkg version file for %s: %w", name, err)
		}
		var selected map[string]json.RawMessage
		for _, candidate := range record.Versions {
			for _, key := range []string{"version", "version-semver", "version-date", "version-string"} {
				var candidateVersion string
				if raw := candidate[key]; len(raw) != 0 && json.Unmarshal(raw, &candidateVersion) == nil && candidateVersion == version.Baseline {
					var candidatePortVersion int
					_ = json.Unmarshal(candidate["port-version"], &candidatePortVersion)
					if candidatePortVersion == version.PortVersion {
						selected = candidate
					}
				}
			}
		}
		if selected == nil {
			return fmt.Errorf("vcpkg version baseline for %s (%s#%d) is absent from its version file", name, version.Baseline, version.PortVersion)
		}
		delete(selected, "git-tree")
		selected["path"], _ = json.Marshal("$/ports/" + name)
		output, err := json.MarshalIndent(map[string]any{"versions": []map[string]json.RawMessage{selected}}, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(versionFile, append(output, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func patchVcpkgPython(root string) error {
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

func compilerEnvironment(paths layout, zig, ccache, python string) map[string]string {
	env := map[string]string{"HPE_ZIG": zig, "CCACHE_DIR": filepath.Join(paths.build, "ccache-cache"),
		"CCACHE_BASEDIR": paths.root, "CCACHE_COMPILERCHECK": "content", "VCPKG_DISABLE_METRICS": "1",
		"VCPKG_DOWNLOADS": filepath.Join(paths.build, "vcpkg-downloads"), "VCPKG_BINARY_SOURCES": "clear",
		"HPE_VCPKG_ROOT":        filepath.Join(paths.build, "vcpkg-root"),
		"VCPKG_MAX_CONCURRENCY": buildJobs(), "PATH": localBuildPath(paths)}
	if ccache != "" {
		env["HPE_CCACHE"] = ccache
	}
	if python != "" {
		env["HPE_PYTHON3"] = python
	}
	return env
}

func localBuildPath(paths layout) string {
	parts := []string{
		filepath.Join(paths.tools, "build-tools", "bin"),
		filepath.Join(paths.tools, "cmake", "bin"),
		filepath.Join(paths.tools, "ninja"),
		filepath.Join(paths.tools, "bootstrap"),
		filepath.Join(paths.tools, "ccache", "bin"),
		filepath.Join(paths.toolchain, "zig"),
		filepath.Join(paths.toolchain, "go", goVersion, "bin"),
		filepath.Join(paths.toolchain, "dotnet"),
	}
	if runtime.GOOS == "linux" {
		parts = append(parts, filepath.Join(paths.tools, "python", "bin"))
	} else if windowsDir := os.Getenv("WINDIR"); windowsDir != "" {
		parts = append(parts, filepath.Join(windowsDir, "System32"))
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

func ensureLinuxHostTools(paths layout, jobs string) error {
	zig := filepath.Join(paths.toolchain, "zig", "zig")
	prefix := filepath.Join(paths.tools, "build-tools")
	sources := filepath.Join(paths.build, "host-tools", "src")
	builds := filepath.Join(paths.build, "host-tools", "build")
	downloads := filepath.Join(paths.build, "downloads", "build-tools")
	marker := filepath.Join(prefix, ".hpe-prefix")
	validPrefix := false
	if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) == prefix {
		for _, relative := range []string{"bin/aclocal", "bin/autoconf", "bin/automake", "bin/libtoolize"} {
			program, readErr := os.ReadFile(filepath.Join(prefix, filepath.FromSlash(relative)))
			if readErr == nil && bytes.Contains(program, []byte(prefix)) {
				validPrefix = true
				break
			}
		}
	}
	if !validPrefix {
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
	if err := installLinuxBaseTools(prefix, downloads); err != nil {
		return err
	}
	if validPrefix && executableExists(filepath.Join(prefix, "bin", "gperf")) {
		if err := rewriteInterpreterShebangs(prefix, prefix); err != nil {
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
		if err := downloadVerified(pkg.url, archive, pkg.hash); err != nil {
			return err
		}
		if err := extractHostTool(archive, filepath.Join(sources, pkg.name), pkg.format, pkg.hash); err != nil {
			return err
		}
		if err := rewriteInterpreterShebangs(filepath.Join(sources, pkg.name), prefix); err != nil {
			return err
		}
	}
	if err := installCondaMake(paths, prefix, downloads); err != nil {
		return err
	}
	pkgM4 := filepath.Join(downloads, "pkg.m4")
	if err := downloadVerified("https://raw.githubusercontent.com/pkgconf/pkgconf/master/pkg.m4", pkgM4,
		"f649256076fa72946d9a844636393128e1c31fd3591389b97aa55705c4df5766"); err != nil {
		return err
	}
	if err := copyFile(pkgM4, filepath.Join(prefix, "share", "aclocal", "pkg.m4"), 0o644); err != nil {
		return err
	}
	localMake := filepath.Join(prefix, "bin", "make")
	if err := configureHostTool(paths, prefix, sources, builds, "m4", jobs, nil); err != nil {
		return err
	}
	if err := configureHostTool(paths, prefix, sources, builds, "autoconf", jobs, map[string]string{"M4": filepath.Join(prefix, "bin", "m4")}); err != nil {
		return err
	}
	if err := configureHostTool(paths, prefix, sources, builds, "automake", jobs, nil); err != nil {
		return err
	}
	if err := configureHostTool(paths, prefix, sources, builds, "libtool", jobs, nil); err != nil {
		return err
	}
	archiveBuild := filepath.Join(builds, "autoconf-archive")
	if _, err := os.Stat(filepath.Join(prefix, "share", "aclocal", "ax_pthread.m4")); err != nil {
		if err := os.MkdirAll(archiveBuild, 0o755); err != nil {
			return err
		}
		if err := runLocalShell(archiveBuild, hostToolEnvironment(paths, zig, prefix), prefix, filepath.Join(sources, "autoconf-archive", "configure"), "--prefix="+prefix); err != nil {
			return err
		}
		if err := rewriteInterpreterShebangs(archiveBuild, prefix); err != nil {
			return err
		}
		if err := runLocalMake(archiveBuild, hostToolEnvironment(paths, zig, prefix), prefix, localMake, "-j"+jobs, "V=1"); err != nil {
			return err
		}
		if err := runLocalMake(archiveBuild, hostToolEnvironment(paths, zig, prefix), prefix, localMake, "install"); err != nil {
			return err
		}
	}
	for _, name := range []string{"bison", "flex", "gperf-3.1"} {
		binary := name
		if name == "gperf-3.1" {
			binary = "gperf"
		}
		if executableExists(filepath.Join(prefix, "bin", binary)) {
			continue
		}
		buildDir := filepath.Join(builds, name)
		if err := os.MkdirAll(buildDir, 0o755); err != nil {
			return err
		}
		configureEnv := hostToolEnvironment(paths, zig, prefix)
		args := []string{"--prefix=" + prefix}
		if name == "bison" {
			configureEnv["M4"] = filepath.Join(prefix, "bin", "m4")
			args = append(args, "--disable-nls")
		} else if name == "flex" {
			configureEnv["M4"] = filepath.Join(prefix, "bin", "m4")
			args = append(args, "--disable-nls")
		} else {
			configureEnv["CXXFLAGS"] = "-O2 -std=gnu++14"
		}
		if err := runLocalShell(buildDir, configureEnv, prefix, filepath.Join(sources, name, "configure"), args...); err != nil {
			return err
		}
		if err := rewriteInterpreterShebangs(buildDir, prefix); err != nil {
			return err
		}
		if err := runLocalMake(buildDir, hostToolEnvironment(paths, zig, prefix), prefix, localMake, "-j"+jobs, "V=1"); err != nil {
			return err
		}
		if err := runLocalMake(buildDir, hostToolEnvironment(paths, zig, prefix), prefix, localMake, "install"); err != nil {
			return err
		}
	}
	if err := os.WriteFile(marker, []byte(prefix+"\n"), 0o644); err != nil {
		return err
	}
	if err := rewriteInterpreterShebangs(prefix, prefix); err != nil {
		return err
	}
	fmt.Printf("Project-local GNU build helpers installed in %s\n", prefix)
	return nil
}

func installLinuxBaseTools(prefix, downloads string) error {
	busyboxName, busyboxHash := "busybox-static_1.38.0-3+b1_amd64.deb", "26801f17e6c88e813be104effc0ea3b43d912bd59ca6295fcd1260528ebb4d41"
	perlArch, perlHash := "amd64", "a9040a81c2c9b2f5e03f39d444ecc6b86e9682f291d54b164b3ef093531f802c"
	if runtime.GOARCH == "arm64" {
		busyboxName, busyboxHash = "busybox-static_1.38.0-3+b1_arm64.deb", "968d1aa8f579fa1ac59c26afa365454369e13cf29848e6400b50028fed0ffda0"
		perlArch, perlHash = "arm64", "b7ecde2320be2ce292db5886894954fd4b10738bed69d4122eec27d46656151a"
	}
	bin := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	busybox := filepath.Join(bin, "busybox")
	busyboxValid := false
	if executableExists(busybox) {
		if output, err := exec.Command(busybox, "--list").Output(); err == nil && len(output) > 0 {
			busyboxValid = true
			fmt.Printf("Reusing validated static BusyBox from %s\n", busybox)
		} else {
			fmt.Printf("Static BusyBox failed its integrity check; restoring from the verified Debian package\n")
			if err := os.Remove(busybox); err != nil {
				return err
			}
		}
	}
	if !busyboxValid {
		deb := filepath.Join(downloads, busyboxName)
		url := "https://deb.debian.org/debian/pool/main/b/busybox/" + busyboxName
		if err := downloadVerified(url, deb, busyboxHash); err != nil {
			return fmt.Errorf("download static BusyBox: %w", err)
		}
		if err := extractDebianDataTar(deb, prefix); err != nil {
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
		if !executableExists(busybox) {
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
	perlDir := filepath.Join(prefix, "perl")
	perl := filepath.Join(perlDir, "bin", "perl")
	perlValid := false
	if executableExists(perl) {
		if output, err := exec.Command(perl, "--version").CombinedOutput(); err == nil && bytes.Contains(output, []byte("Perl")) {
			perlValid = true
			fmt.Printf("Reusing validated relocatable Perl from %s\n", perl)
		} else {
			fmt.Printf("Relocatable Perl failed its version check; restoring from the verified archive\n")
			if err := os.Remove(perl); err != nil {
				return err
			}
		}
	}
	if !perlValid {
		archiveName := "perl-linux-" + perlArch + ".tar.xz"
		archive := filepath.Join(downloads, archiveName)
		url := "https://github.com/skaji/relocatable-perl/releases/download/5.44.0.0/" + archiveName
		if err := downloadVerified(url, archive, perlHash); err != nil {
			return fmt.Errorf("download relocatable Perl: %w", err)
		}
		staging := perlDir + ".staging"
		_ = os.RemoveAll(staging)
		if err := os.MkdirAll(staging, 0o755); err != nil {
			return err
		}
		if err := extractTarXZ(archive, staging, 1); err != nil {
			return err
		}
		if !executableExists(filepath.Join(staging, "bin", "perl")) {
			return errors.New("relocatable Perl archive did not contain bin/perl")
		}
		if err := replaceDirectory(staging, perlDir); err != nil {
			return err
		}
	}
	perlLink := filepath.Join(bin, "perl")
	if _, err := os.Lstat(perlLink); errors.Is(err, os.ErrNotExist) {
		if err := os.Symlink(filepath.Join("..", "perl", "bin", "perl"), perlLink); err != nil {
			return err
		}
	}
	return nil
}

func rewriteInterpreterShebangs(root, prefix string) error {
	marker := filepath.Join(root, ".hpe-interpreters-patched")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	shell := filepath.Join(prefix, "bin", "sh")
	perl := filepath.Join(prefix, "bin", "perl")
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		line, readErr := io.ReadAll(io.LimitReader(file, 256))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
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

func runLocalShell(directory string, env map[string]string, prefix, script string, args ...string) error {
	return runCommand(directory, env, filepath.Join(prefix, "bin", "sh"), append([]string{script}, args...)...)
}

func runLocalMake(directory string, env map[string]string, prefix, make string, args ...string) error {
	args = append(args, "SHELL="+filepath.Join(prefix, "bin", "sh"))
	return runCommand(directory, env, make, args...)
}

func extractDebianDataTar(deb, destination string) error {
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
		sizeText := strings.TrimSpace(string(header[48:58]))
		var size int
		if _, err := fmt.Sscanf(sizeText, "%d", &size); err != nil || size < 0 || offset+60+size > len(data) {
			return errors.New("invalid Debian archive member size")
		}
		name := strings.TrimSpace(strings.TrimSuffix(string(header[:16]), "/"))
		member := data[offset+60 : offset+60+size]
		if strings.HasPrefix(name, "data.tar") {
			var reader io.Reader = bytes.NewReader(member)
			switch {
			case strings.HasSuffix(name, ".xz"):
				xzReader, err := xz.NewReader(reader)
				if err != nil {
					return err
				}
				reader = xzReader
			case strings.HasSuffix(name, ".gz"):
				gzReader, err := gzip.NewReader(reader)
				if err != nil {
					return err
				}
				defer gzReader.Close()
				reader = gzReader
			default:
				return fmt.Errorf("unsupported Debian data archive %s", name)
			}
			return extractTar(reader, destination, 0)
		}
		offset += 60 + size
		if offset%2 != 0 {
			offset++
		}
	}
	return errors.New("Debian archive has no data.tar member")
}

func hostToolEnvironment(paths layout, zig, prefix string) map[string]string {
	cc, cxx := wrapperPaths(paths)
	zigAr := filepath.Join(paths.tools, "bootstrap", "hpe-zig-ar")
	zigRanlib := filepath.Join(paths.tools, "bootstrap", "hpe-zig-ranlib")
	zigNm := filepath.Join(paths.tools, "bootstrap", "hpe-zig-nm")
	zigLd := filepath.Join(paths.tools, "bootstrap", "hpe-zig-ld")
	return map[string]string{"CC": cc, "CXX": cxx, "CC_FOR_BUILD": cc, "CXX_FOR_BUILD": cxx,
		"AR": zigAr, "RANLIB": zigRanlib, "NM": zigNm, "LD": zigLd, "HPE_ZIG": zig,
		"CFLAGS": "-O2", "CXXFLAGS": "-O2", "AWK": filepath.Join(prefix, "bin", "awk"),
		"GREP": filepath.Join(prefix, "bin", "grep"), "EGREP": filepath.Join(prefix, "bin", "grep") + " -E",
		"FGREP": filepath.Join(prefix, "bin", "grep") + " -F", "TAR": filepath.Join(prefix, "bin", "tar"),
		"PATH": filepath.Join(prefix, "bin"),
		"PERL": filepath.Join(prefix, "bin", "perl"), "CONFIG_SHELL": filepath.Join(prefix, "bin", "sh"),
		"SHELL": filepath.Join(prefix, "bin", "sh"), "ACLOCAL_PATH": filepath.Join(prefix, "share", "aclocal")}
}

func configureHostTool(paths layout, prefix, sources, builds, name, jobs string, extra map[string]string) error {
	binary := name
	if name == "libtool" {
		binary = "libtoolize"
	}
	if executableExists(filepath.Join(prefix, "bin", binary)) {
		fmt.Printf("Reusing installed Linux host tool: %s\n", filepath.Join(prefix, "bin", binary))
		return nil
	}
	buildDir := filepath.Join(builds, name)
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}
	env := hostToolEnvironment(paths, filepath.Join(paths.toolchain, "zig", "zig"), prefix)
	for key, value := range extra {
		env[key] = value
	}
	args := []string{"--prefix=" + prefix}
	if name == "m4" {
		args = append(args, "--with-syscmd-shell="+filepath.Join(prefix, "bin", "sh"))
	}
	if err := runStep("Configure Linux host tool "+name, func() error {
		return runLocalShell(buildDir, env, prefix, filepath.Join(sources, name, "configure"), args...)
	}); err != nil {
		return err
	}
	make := filepath.Join(prefix, "bin", "make")
	if err := rewriteInterpreterShebangs(buildDir, prefix); err != nil {
		return err
	}
	if err := runStep("Compile Linux host tool "+name, func() error { return runLocalMake(buildDir, env, prefix, make, "-j"+jobs, "V=1") }); err != nil {
		return err
	}
	return runStep("Install Linux host tool "+name, func() error { return runLocalMake(buildDir, env, prefix, make, "install") })
}

func extractHostTool(archive, destination, format, digest string) error {
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
		err = extractTarXZ(archive, destination, 1)
	case "tar.gz":
		err = extractTarGz(archive, destination, 1)
	default:
		err = fmt.Errorf("unsupported host tool archive format %q", format)
	}
	if err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(digest+"\n"), 0o644)
}

func installCondaMake(paths layout, prefix, downloads string) error {
	installed := filepath.Join(prefix, "bin", "make")
	if executableExists(installed) {
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
	if err := downloadVerified(url, archive, digest); err != nil {
		return fmt.Errorf("download GNU Make: %w", err)
	}
	packageArchive, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer packageArchive.Close()
	for _, entry := range packageArchive.File {
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
		err = extractTar(decoder, prefix, 0)
		decoder.Close()
		_ = compressed.Close()
		if err != nil {
			return err
		}
		if !executableExists(installed) {
			return errors.New("GNU Make package did not contain bin/make")
		}
		return nil
	}
	return errors.New("GNU Make package did not contain a compressed package archive")
}

func configureBuildInstall(paths layout, cmake, ninja, zig, dotnet, ccache string, jobs string) error {
	rid := hostRuntimeID()
	installRoot := envPath("HPE_INSTALL_ROOT", filepath.Join(paths.root, "install", rid))
	projectBuild := filepath.Join(paths.build, "projects", rid, "Release")
	fmt.Printf("Target runtime: %s; parallel jobs: %s; incremental CMake/Ninja directory: %s\n", rid, jobs, projectBuild)
	var manifestDir string
	if err := runStep("Prepare vcpkg manifest and local registry configuration", func() error {
		var err error
		manifestDir, err = prepareVcpkgManifest(paths)
		return err
	}); err != nil {
		return err
	}
	cc, cxx := wrapperPaths(paths)
	python := ""
	if runtime.GOOS == "linux" {
		python = filepath.Join(paths.tools, "python", "bin", "python3")
	}
	env := compilerEnvironment(paths, zig, ccache, python)
	if runtime.GOARCH == "arm64" {
		env["HPE_VCPKG_ARCH"] = "arm64"
	} else {
		env["HPE_VCPKG_ARCH"] = "x64"
	}
	env["HPE_CC"], env["HPE_CXX"] = cc, cxx
	env["CC"], env["CXX"] = cc, cxx
	env["PATH"] = localBuildPath(paths)
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
	args := []string{"-S", paths.root, "-B", projectBuild, "-G", "Ninja", "-DCMAKE_MAKE_PROGRAM=" + ninja,
		"-DCMAKE_BUILD_TYPE=Release", "-DCMAKE_C_COMPILER=" + cc, "-DCMAKE_C_COMPILER_LAUNCHER=" + ccache,
		"-DVCPKG_APPLOCAL_DEPS=ON", "-DVCPKG_USE_LEGACY_APPLOCAL=OFF",
		"-DCMAKE_TOOLCHAIN_FILE=" + filepath.Join(vcpkg, "scripts", "buildsystems", "vcpkg.cmake"),
		"-DVCPKG_ROOT=" + vcpkg, "-DVCPKG_TARGET_TRIPLET=" + triplet, "-DVCPKG_HOST_TRIPLET=" + triplet,
		"-DVCPKG_OVERLAY_TRIPLETS=" + filepath.Join(paths.root, "cmake", "vcpkg", "triplets"),
		"-DVCPKG_OVERLAY_PORTS=" + filepath.Join(paths.root, "cmake", "vcpkg", "overlay-ports"),
		"-DVCPKG_MANIFEST_DIR=" + manifestDir,
		"-DVCPKG_INSTALLED_DIR=" + filepath.Join(projectBuild, "vcpkg_installed"),
		"-DVCPKG_MANIFEST_INSTALL=ON", "-DVCPKG_MANIFEST_MODE=ON", "-DCMAKE_INSTALL_PREFIX=" + installRoot}
	if err := runStep("Configure HPE and resolve pinned native dependencies", func() error { return runCommand(paths.root, env, cmake, args...) }); err != nil {
		return err
	}
	if err := runStep("Incrementally compile native simulator, CLI, and GUI bridge", func() error {
		return runCommand(paths.root, env, cmake, "--build", projectBuild, "--parallel", jobs, "--verbose")
	}); err != nil {
		return err
	}
	if err := runStep("Refresh native install files", func() error {
		if err := os.RemoveAll(installRoot); err != nil {
			return err
		}
		if err := os.MkdirAll(installRoot, 0o755); err != nil {
			return err
		}
		return runCommand(paths.root, env, cmake, "--install", projectBuild)
	}); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		if err := runStep("Bundle compiler runtime libraries for Linux", func() error { return bundleLinuxRuntime(zig, installRoot) }); err != nil {
			return err
		}
	}
	if err := runStep("Incrementally publish the self-contained desktop GUI", func() error {
		return publishGui(paths, dotnet, installRoot, rid, jobs)
	}); err != nil {
		return err
	}
	if err := runStep("Copy protocol examples and bundled fonts", func() error {
		if err := copyDirectory(filepath.Join(paths.root, "examples"), filepath.Join(installRoot, "gui", "examples")); err != nil {
			return err
		}
		return copyDirectory(filepath.Join(paths.root, "assets", "fonts"), filepath.Join(installRoot, "lib", "fonts"))
	}); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		return runStep("Write Linux GUI launcher", func() error { return writeLinuxLauncher(installRoot) })
	}
	launcher := []byte("@echo off\r\nset ROOT=%~dp0\r\n\"%ROOT%gui\\hpe-gui.exe\" %*\r\n")
	return runStep("Write Windows GUI launcher", func() error { return os.WriteFile(filepath.Join(installRoot, "hpe-gui.cmd"), launcher, 0o644) })
}

func hostRuntimeID() string {
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

func prepareVcpkgManifest(paths layout) (string, error) {
	manifestDir := filepath.Join(paths.build, "manifests")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		return "", err
	}
	manifest, err := os.ReadFile(filepath.Join(paths.root, "vcpkg.json"))
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "vcpkg.json"), manifest, 0o644); err != nil {
		return "", err
	}
	relativeRegistry, err := filepath.Rel(manifestDir, filepath.Join(paths.build, "vcpkg-root"))
	if err != nil {
		return "", err
	}
	configuration := map[string]any{"default-registry": map[string]string{
		"kind": "filesystem", "path": filepath.ToSlash(relativeRegistry)}}
	data, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		return "", err
	}
	return manifestDir, os.WriteFile(filepath.Join(manifestDir, "vcpkg-configuration.json"), append(data, '\n'), 0o644)
}

func wrapperPaths(paths layout) (string, string) {
	base := filepath.Join(paths.tools, "bootstrap")
	cc, cxx := filepath.Join(base, "hpe-zig-cc"), filepath.Join(base, "hpe-zig-cxx")
	if runtime.GOOS == "windows" {
		cc += ".exe"
		cxx += ".exe"
	}
	return cc, cxx
}

func bundleLinuxRuntime(zig, installRoot string) error {
	lib := filepath.Join(installRoot, "lib")
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
		if err := copyFile(source, filepath.Join(lib, name), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func publishGui(paths layout, dotnet, installRoot, rid, jobs string) error {
	guiOut := filepath.Join(installRoot, "gui")
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
		"-m:" + jobs, "-p:PublishSingleFile=false", "-p:PublishTrimmed=false", "-o", guiOut}
	if err := runCommand(paths.root, env, dotnet, args...); err != nil {
		return err
	}
	fontdir := filepath.Join(guiOut, "fonts")
	if err := os.RemoveAll(fontdir); err != nil {
		return err
	}
	return nil
}

func writeLinuxLauncher(installRoot string) error {
	path := filepath.Join(installRoot, "hpe-gui")
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return copyFile(executable, path, 0o755)
}

func runLinuxGuiLauncher(args []string) int {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := filepath.Dir(executable)
	libraryPath := filepath.Join(root, "lib") + string(os.PathListSeparator) + filepath.Join(root, "gui")
	if inherited := os.Getenv("LD_LIBRARY_PATH"); inherited != "" {
		libraryPath += string(os.PathListSeparator) + inherited
	}
	if err := os.Setenv("LD_LIBRARY_PATH", libraryPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	userID := "default"
	if currentUser, err := user.Current(); err == nil && currentUser.Uid != "" {
		userID = currentUser.Uid
	}
	cacheDir := filepath.Join(os.TempDir(), "hpe-fontconfig-cache-"+userID)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var escapedRoot bytes.Buffer
	if err := xml.EscapeText(&escapedRoot, []byte(filepath.Join(root, "lib", "fonts"))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var escapedCache bytes.Buffer
	if err := xml.EscapeText(&escapedCache, []byte(cacheDir)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fontConfig := fmt.Sprintf("<fontconfig><dir>%s</dir><cachedir>%s</cachedir><alias><family>sans-serif</family><prefer><family>DejaVu Sans</family><family>Noto Sans SC</family></prefer></alias><alias><family>monospace</family><prefer><family>DejaVu Sans</family></prefer></alias></fontconfig>\n", escapedRoot.String(), escapedCache.String())
	configFile, err := os.CreateTemp(os.TempDir(), "hpe-fontconfig-*.conf")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	configPath := configFile.Name()
	defer os.Remove(configPath)
	if _, err := configFile.WriteString(fontConfig); err != nil {
		_ = configFile.Close()
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := configFile.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.Setenv("FONTCONFIG_FILE", configPath); err != nil {
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

func runCommand(directory string, overrides map[string]string, name string, args ...string) error {
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
	fmt.Printf("\n>>> [cwd=%q] %s\n", directory, formatCommand(name, args))
	envKeys := make([]string, 0, len(overrides))
	for key := range overrides {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		fmt.Printf("    env %s=%q\n", key, overrides[key])
	}
	if err := command.Run(); err != nil {
		fmt.Printf("<<< FAILED after %s: %v\n", time.Since(started).Round(time.Millisecond), err)
		return fmt.Errorf("command %s failed: %w", name, err)
	}
	fmt.Printf("<<< OK after %s: %s\n", time.Since(started).Round(time.Millisecond), name)
	return nil
}

func executableExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func copyFile(source, destination string, mode os.FileMode) error {
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
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(destination, mode)
}

func copyDirectory(source, destination string) error {
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
		return copyFile(path, target, mode)
	})
}

func projectLayout() (layout, error) {
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
	build := envPath("HPE_BUILD_ROOT", filepath.Join(root, "build"))
	toolchain := envPath("HPE_TOOL_ROOT", filepath.Join(root, "toolchain"))
	return layout{root, build, filepath.Join(build, "tools"), filepath.Join(build, "downloads"), toolchain}, nil
}

func envPath(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		if absolute, err := filepath.Abs(value); err == nil {
			return absolute
		}
	}
	return fallback
}

func pinnedArtifacts(p layout) ([]artifact, error) {
	arch := runtime.GOARCH
	zigOS, cmakeOS, ninjaName, zigHash, cmakeHash, ninjaHash := "", "", "", "", "", ""
	var pythonArch, pythonHash string
	switch runtime.GOOS {
	case "linux":
		zigOS, cmakeOS = "linux", "linux"
		switch arch {
		case "amd64":
			zigHash = "1cbe9df9f27e6b78d14ccbca43b6703a404ef79ef1c463de901d7f088d4e2026"
			cmakeHash = "e5bb807f7728cb60cd8b27ebc97a2edb469b68655f21e844a600c3575b76f5bb"
			ninjaName, ninjaHash = "ninja-linux.zip", "0830252db77884957a1a4b87b05a1e2d9b5f658b8367f82999a941884cbe0238"
			pythonArch, pythonHash = "x86_64", "c0ccda275948c79996e46993c2c5476ff5cca606dee530f1dea712179131b348"
		case "arm64":
			zigHash = "9e8d11661d4ae3bd57702a3832781e23ad151dde5798e16a5ccd503f65234ff8"
			cmakeHash = "a1b6cc63636a0e55c63257cf3315a8a5f129e42fade25db1afea4ff8ab06f25e"
			ninjaName, ninjaHash = "ninja-linux-aarch64.zip", "740f1b9f9d8ae68438240a6a2f3f7a27fc8b1946d2024a6a6b25857ee877987b"
			pythonArch, pythonHash = "aarch64", "baa3d107d17e4328448e30c3c9c83cca0eb41ca7a37c10982e14d46a5c3db07d"
		default:
			return nil, fmt.Errorf("unsupported Linux architecture %s", arch)
		}
	case "windows":
		zigOS, cmakeOS = "windows", "windows"
		switch arch {
		case "amd64":
			zigHash = "b5663f69581dcf391293fbf16c06cb80d81d806545ce618b4d0bab7f0eb8c428"
			cmakeHash = "bace36e94b31c68ab6fa295f26dfa11219e0701cf7c94b0284a7d1cb13dac536"
			ninjaName, ninjaHash = "ninja-win.zip", "26a40fa8595694dec2fad4911e62d29e10525d2133c9a4230b66397774ae25bf"
		case "arm64":
			zigHash = "0a59d91fa1cb40cf068e9b0954434ce973500c7a2ea749f1e01af62cdab52d26"
			cmakeHash = "ed673bbc4eb7c1e59b0407fd52f1680fc0ae3229a46b9b7b3ea9133764f07f67"
			ninjaName, ninjaHash = "ninja-winarm64.zip", "fb959b674970e36a7c9a23191524b80fb5298fc71fc98bfa42456bcc0a8dfb2f"
		default:
			return nil, fmt.Errorf("unsupported Windows architecture %s", arch)
		}
	default:
		return nil, fmt.Errorf("unsupported host operating system %s", runtime.GOOS)
	}
	zigFile := fmt.Sprintf("zig-%s-%s-%s.zip", archName(arch), zigOS, zigVersion)
	zigFormat, zigStrip := "zip", 1
	if runtime.GOOS == "linux" {
		zigFile = fmt.Sprintf("zig-%s-%s-%s.tar.xz", archName(arch), zigOS, zigVersion)
		zigFormat, zigStrip = "tar.xz", 1
	}
	cmakeArch := archName(arch)
	if runtime.GOOS == "windows" && arch == "arm64" {
		cmakeArch = "arm64"
	}
	cmakeFile := fmt.Sprintf("cmake-%s-%s-%s.tar.gz", cmakeVersion, cmakeOS, cmakeArch)
	cmakeFormat, cmakeStrip := "tar.gz", 1
	if runtime.GOOS == "windows" {
		cmakeFile = fmt.Sprintf("cmake-%s-windows-%s.zip", cmakeVersion, cmakeArch)
		cmakeFormat, cmakeStrip = "zip", 1
	}
	items := []artifact{
		{name: zigFile, url: "https://ziglang.org/download/" + zigVersion + "/" + zigFile, sha256: zigHash, destination: filepath.Join(p.toolchain, "zig"), format: zigFormat, strip: zigStrip},
		{name: cmakeFile, url: "https://cmake.org/files/v4.4/" + cmakeFile, sha256: cmakeHash, destination: filepath.Join(p.tools, "cmake"), format: cmakeFormat, strip: cmakeStrip},
		{name: ninjaName, url: "https://github.com/ninja-build/ninja/releases/download/v" + ninjaVersion + "/" + ninjaName, sha256: ninjaHash, destination: filepath.Join(p.tools, "ninja"), format: "zip"},
	}
	if runtime.GOOS == "linux" {
		pythonFile := fmt.Sprintf("cpython-%s+20251014-%s-unknown-linux-gnu-install_only_stripped.tar.gz", pythonVersion, pythonArch)
		items = append(items, artifact{name: pythonFile, url: "https://github.com/astral-sh/python-build-standalone/releases/download/20251014/" + pythonFile, sha256: pythonHash, destination: filepath.Join(p.tools, "python"), format: "tar.gz", strip: 1})
	}
	goSDK, err := goArtifact(p)
	if err != nil {
		return nil, err
	}
	items = append(items, goSDK)
	dotnet, err := dotnetArtifact(p)
	if err != nil {
		return nil, err
	}
	items = append(items, dotnet)
	for index := range items {
		if items[index].sha256 == "" {
			if artifactInstalled(items[index]) {
				continue
			}
			checksum, err := officialCMakeChecksum(items[index].name)
			if err != nil {
				return nil, err
			}
			items[index].sha256 = checksum
		}
	}
	return items, nil
}

func archName(arch string) string {
	if arch == "amd64" {
		return "x86_64"
	}
	if arch == "arm64" {
		return "aarch64"
	}
	return arch
}

type dotnetRelease struct {
	Releases []struct {
		SDKs []struct {
			Version string `json:"version"`
			Files   []struct {
				RID  string `json:"rid"`
				Name string `json:"name"`
				URL  string `json:"url"`
				Hash string `json:"hash"`
			} `json:"files"`
		} `json:"sdks"`
	} `json:"releases"`
}

func dotnetArtifact(p layout) (artifact, error) {
	destination := filepath.Join(p.toolchain, "dotnet")
	dotnetBinary := filepath.Join(destination, "dotnet")
	if runtime.GOOS == "windows" {
		dotnetBinary += ".exe"
	}
	if digest, ok := installedArtifactDigest(destination, dotnetBinary); ok {
		return artifact{name: "dotnet-sdk-" + dotnetVersion, sha256: digest, destination: destination}, nil
	}
	url := "https://builds.dotnet.microsoft.com/dotnet/release-metadata/10.0/releases.json"
	data, err := getBytes(url)
	if err != nil {
		return artifact{}, err
	}
	var metadata dotnetRelease
	if err := json.Unmarshal(data, &metadata); err != nil {
		return artifact{}, fmt.Errorf("parse .NET release metadata: %w", err)
	}
	ridArch := "x64"
	if runtime.GOARCH == "arm64" {
		ridArch = "arm64"
	}
	rid := runtime.GOOS + "-" + ridArch
	for _, release := range metadata.Releases {
		for _, sdk := range release.SDKs {
			if sdk.Version != dotnetVersion {
				continue
			}
			for _, file := range sdk.Files {
				if file.RID == rid && (file.Name == "sdk" || strings.HasPrefix(file.Name, "sdk-")) {
					format := "tar.gz"
					if strings.HasSuffix(file.URL, ".zip") {
						format = "zip"
					}
					return artifact{name: filepath.Base(file.URL), url: file.URL, sha256: strings.ToLower(file.Hash), destination: filepath.Join(p.toolchain, "dotnet"), format: format}, nil
				}
			}
		}
	}
	return artifact{}, fmt.Errorf(".NET SDK %s archive for %s was not found in official metadata", dotnetVersion, rid)
}

type goRelease struct {
	Version string `json:"version"`
	Files   []struct {
		Filename string `json:"filename"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		Kind     string `json:"kind"`
		SHA256   string `json:"sha256"`
	} `json:"files"`
}

func goArtifact(p layout) (artifact, error) {
	destination := filepath.Join(p.toolchain, "go", goVersion)
	binary := filepath.Join(destination, "bin", "go")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if digest, ok := installedArtifactDigest(destination, binary); ok {
		return artifact{name: "Go SDK " + goVersion, sha256: digest, destination: destination}, nil
	}
	data, err := getBytes("https://go.dev/dl/?mode=json")
	if err != nil {
		return artifact{}, err
	}
	var releases []goRelease
	if err := json.Unmarshal(data, &releases); err != nil {
		return artifact{}, fmt.Errorf("parse Go release metadata: %w", err)
	}
	goOS, goArch := runtime.GOOS, runtime.GOARCH
	version := "go" + goVersion
	for _, release := range releases {
		if release.Version != version {
			continue
		}
		for _, file := range release.Files {
			if file.OS == goOS && file.Arch == goArch && file.Kind == "archive" {
				format, strip := "tar.gz", 1
				if strings.HasSuffix(file.Filename, ".zip") {
					format, strip = "zip", 1
				}
				return artifact{name: file.Filename, url: "https://go.dev/dl/" + file.Filename,
					sha256: strings.ToLower(file.SHA256), destination: filepath.Join(p.toolchain, "go", goVersion), format: format, strip: strip}, nil
			}
		}
	}
	return artifact{}, fmt.Errorf("Go SDK %s archive for %s/%s was not found in official metadata", version, goOS, goArch)
}

func installedArtifactDigest(destination, candidate string) (string, bool) {
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

func officialCMakeChecksum(filename string) (string, error) {
	data, err := getBytes("https://cmake.org/files/v4.4/cmake-" + cmakeVersion + "-SHA-256.txt")
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

func installArtifact(item artifact, downloads string) error {
	if artifactInstalled(item) {
		fmt.Printf("Reusing complete installed artifact %s (verified SHA-256 %s)\n", item.name, item.sha256)
		return nil
	}
	if item.sha256 == "" {
		return fmt.Errorf("refusing to use or install %s without a pinned SHA-256", item.name)
	}
	archive := filepath.Join(downloads, item.name)
	if err := downloadVerified(item.url, archive, item.sha256); err != nil {
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
	if err := extractArchive(archive, tmp, item.format, item.strip); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".hpe-artifact-sha256"), []byte(strings.ToLower(item.sha256)+"\n"), 0o644); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := replaceDirectory(tmp, item.destination); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	fmt.Printf("Installed %s from a SHA-256 verified archive\n", item.name)
	return nil
}

func artifactInstalled(item artifact) bool {
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
	case strings.Contains(filepath.Base(item.destination), "1.27.2"):
		candidate = filepath.Join(item.destination, "bin", "go")
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
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

func downloadVerified(url, destination, expected string) error {
	if expected == "" {
		return errors.New("refusing to install an archive without a SHA-256")
	}
	info, statErr := os.Stat(destination)
	if statErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
		if err := verifyDigest(destination, expected); err == nil {
			fmt.Printf("Cached archive verified; skipping download: %s (SHA-256 %s)\n", filepath.Base(destination), expected)
			return nil
		}
		fmt.Printf("Cached archive failed SHA-256 verification; downloading again: %s\n", filepath.Base(destination))
		if err := os.Remove(destination); err != nil {
			return fmt.Errorf("remove invalid cached archive %s: %w", destination, err)
		}
	} else {
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if statErr == nil {
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
		if err := downloadOnce(url, tmp); err != nil {
			last = err
			fmt.Printf("Download attempt %d failed: %v\n", attempt, err)
			_ = os.Remove(tmp)
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}
		if err := verifyDigest(tmp, expected); err != nil {
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

func downloadOnce(url, destination string) error {
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
	_, copyErr := io.Copy(file, response.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func getBytes(url string) ([]byte, error) {
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

func verifyDigest(path, expected string) error {
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

func extractArchive(archive, destination, format string, strip int) error {
	switch format {
	case "zip":
		return extractZip(archive, destination, strip)
	case "tar.gz":
		return extractTarGz(archive, destination, strip)
	case "tar.xz":
		return extractTarXZ(archive, destination, strip)
	default:
		return fmt.Errorf("unsupported archive format %q", format)
	}
}

func extractZip(archive, destination string, strip int) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		name, skip, err := safeArchivePath(file.Name, strip)
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
		_, copyErr := io.Copy(output, source)
		closeErr := output.Close()
		_ = source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func extractTarGz(archive, destination string, strip int) error {
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
	return extractTar(gz, destination, strip)
}

func extractTarXZ(archive, destination string, strip int) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := xz.NewReader(file)
	if err != nil {
		return err
	}
	return extractTar(reader, destination, strip)
}

func extractTar(source io.Reader, destination string, strip int) error {
	reader := tar.NewReader(source)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name, skip, err := safeArchivePath(header.Name, strip)
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
			_, copyErr := io.Copy(output, reader)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
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
			linkTarget := filepath.Clean(filepath.Join(filepath.Dir(target), filepath.FromSlash(header.Linkname)))
			base, err := filepath.Abs(destination)
			if err != nil {
				return err
			}
			resolved, err := filepath.Abs(linkTarget)
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
			linkName, skip, err := safeArchivePath(header.Linkname, strip)
			if err != nil {
				return err
			}
			if skip {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Link(filepath.Join(destination, linkName), target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported tar entry type %d in %s", header.Typeflag, header.Name)
		}
	}
}

func safeArchivePath(name string, strip int) (string, bool, error) {
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

func replaceDirectory(staged, destination string) error {
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
