// File: Native.cs
// Purpose: Project-owned GUI component for apps / gui / Hpe.Gui / Native.cs.
// Author: Elrond-Xie
// Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.

using System.Reflection;
using System.Runtime.InteropServices;
using System.Text;

namespace hpe.gui;

/// <summary>Represents the native api type.</summary>
internal static unsafe class native_api
{
    private const string dll_name = "hpe_gui_bridge";
    private static IntPtr native_search_cookie;

    /// <summary>Registers the platform-specific resolver for the bundled native bridge.</summary>
    static native_api()
    {
        NativeLibrary.SetDllImportResolver(typeof(native_api).Assembly,
                                           resolve_library);
    }

    /// <summary>Resolves the native bridge from the shared library directory.</summary>
    private static IntPtr resolve_library(string name, Assembly assembly,
                                          DllImportSearchPath? search_path)
    {
        if (name != dll_name) {
            return IntPtr.Zero;
        }
        string[] library_names = OperatingSystem.IsWindows()
                                     ? new[] { "libhpe_gui_bridge.dll", "hpe_gui_bridge.dll" }
                                     : new[] { "libhpe_gui_bridge.so" };
        string[] directories = {
            Path.Combine(AppContext.BaseDirectory, "lib"),
            AppContext.BaseDirectory,
            Path.GetFullPath(Path.Combine(AppContext.BaseDirectory, "..", "lib"))
        };
        if (OperatingSystem.IsWindows() && native_search_cookie == IntPtr.Zero) {
            native_search_cookie = add_dll_directory(directories[0]);
            if (native_search_cookie == IntPtr.Zero) {
                native_search_cookie = add_dll_directory(directories[2]);
            }
        }
        foreach (string directory in directories) {
            foreach (string library_name in library_names) {
                string path = Path.Combine(directory, library_name);
                if (File.Exists(path)) {
                    return NativeLibrary.Load(path);
                }
            }
        }
        return IntPtr.Zero;
    }

    /// <summary>Adds a directory to the Windows native library search path.</summary>
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, EntryPoint = "AddDllDirectory")]
    private static extern IntPtr add_dll_directory(string directory);

    /// <summary>Represents the native event type.</summary>
    [StructLayout(LayoutKind.Sequential)]
    internal struct native_event
    {
        public ulong time, data_start;
        public int kind, protocol;
        public byte scl, sda, clock, mosi, miso, chip_select, value, read;
        public fixed byte lines[8];
        public byte line_count;
        public fixed byte message[96];
    }
    /// <summary>Represents the native device type.</summary>
    [StructLayout(LayoutKind.Sequential)]
    internal struct native_device
    {
        public int protocol;
        public fixed byte name[48];
        public uint locator, identity, memory_size;
        public byte state, flags;
        /// <summary>Performs the get name operation.</summary>
        public string get_name()
        {
            native_device copy = this;
            byte *p = copy.name;
            return decode_utf8(p, 48);
        }
    }
    /// <summary>Creates a simulator instance through the native bridge.</summary>
    [DllImport(dll_name, CallingConvention = CallingConvention.Cdecl)]
    internal static extern IntPtr hpe_gui_sim_create();
    /// <summary>Destroys a simulator instance through the native bridge.</summary>
    [DllImport(dll_name, CallingConvention = CallingConvention.Cdecl)]
    internal static extern void hpe_gui_sim_destroy(IntPtr sim);
    /// <summary>Runs a scenario and returns its native trace or error message.</summary>
    [DllImport(dll_name, CallingConvention = CallingConvention.Cdecl)]
    internal static extern int
    hpe_gui_scenario_run(IntPtr sim, [MarshalAs(UnmanagedType.LPUTF8Str)] string text, byte[] error,
                         UIntPtr size);
    /// <summary>Returns the number of recorded native events.</summary>
    [DllImport(dll_name, CallingConvention = CallingConvention.Cdecl)]
    internal static extern UIntPtr hpe_gui_event_count(IntPtr sim);
    /// <summary>Copies one native event into the managed representation.</summary>
    [DllImport(dll_name, CallingConvention = CallingConvention.Cdecl)]
    internal static extern int hpe_gui_event_get(IntPtr sim, UIntPtr index, out native_event ev);
    /// <summary>Returns the number of software modeled devices.</summary>
    [DllImport(dll_name, CallingConvention = CallingConvention.Cdecl)]
    internal static extern UIntPtr hpe_gui_device_count(IntPtr sim);
    /// <summary>Copies one software modeled device description.</summary>
    [DllImport(dll_name, CallingConvention = CallingConvention.Cdecl)]
    internal static extern int hpe_gui_device_get(IntPtr sim, UIntPtr index,
                                                  out native_device device);
    /// <summary>Reads bytes from a software modeled device's memory.</summary>
    [DllImport(dll_name, CallingConvention = CallingConvention.Cdecl)]
    internal static extern int hpe_gui_device_read(IntPtr sim, UIntPtr index, uint offset,
                                                   byte[] data, UIntPtr length);
    /// <summary>Performs the decode utf8 operation.</summary>
    internal static string decode_utf8(byte *p, int n)
    {
        int len = 0;
        while (len < n && p[len] != 0) {
            len++;
        }
        return Encoding.UTF8.GetString(p, len);
    }
    /// <summary>Performs the message operation.</summary>
    internal static string message(native_event e)
    {
        byte *p = e.message;
        return decode_utf8(p, 96);
    }
    /// <summary>Performs the get line operation.</summary>
    internal static byte get_line(native_event e, int i)
    {
        byte *p = e.lines;
        return p[i];
    }
}
