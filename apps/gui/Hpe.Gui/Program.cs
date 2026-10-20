// File: Program.cs
// Purpose: Project-owned GUI component for apps / gui / Hpe.Gui / Program.cs.
// Author: Elrond-Xie
// Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.

using Avalonia;
using System.Runtime.InteropServices;

namespace hpe.gui;

/// <summary>Represents the hpe program type.</summary>
internal static class hpe_program
{
    /// <summary>Performs the Main operation.</summary>
    [STAThread]
    public static int Main(string[] args)
    {
        hpe_app.smoke_test = Array.IndexOf(args, "--smoke-test") >= 0;
        args = args.Where(a => a != "--smoke-test").ToArray();
        if (OperatingSystem.IsWindows()) {
            string fonts =
                Path.GetFullPath(Path.Combine(AppContext.BaseDirectory, "..", "lib", "fonts"));
            if (Directory.Exists(fonts)) {
                foreach (string file in Directory.EnumerateFiles(fonts, "*.*")) {
                    if (Path.GetExtension(file).Equals(".ttf",
                                                       StringComparison.OrdinalIgnoreCase) ||
                        Path.GetExtension(file).Equals(".otf",
                                                       StringComparison.OrdinalIgnoreCase)) {
                        add_font_resource_ex(file, 0x10, IntPtr.Zero);
                    }
                }
            }
        }
        return build_avalonia_app().StartWithClassicDesktopLifetime(args);
    }
    /// <summary>Performs the add font resource ex operation.</summary>
    [DllImport("gdi32.dll", CharSet = CharSet.Unicode, EntryPoint = "AddFontResourceExW")]
    private static extern int add_font_resource_ex(string name, uint flags, IntPtr reserved);
    /// <summary>Builds the Avalonia application with the platform-specific backend.</summary>
    public static AppBuilder
    build_avalonia_app() => AppBuilder.Configure<hpe_app>().UsePlatformDetect().LogToTrace();
}
