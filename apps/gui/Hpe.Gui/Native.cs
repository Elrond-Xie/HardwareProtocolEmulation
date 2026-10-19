using System.Runtime.InteropServices;
using System.Text;

namespace Hpe.Gui;

internal static unsafe class Native
{
    private const string Dll = "hpe_gui_bridge";
    [StructLayout(LayoutKind.Sequential)] internal struct Event
    {
        public ulong Time, DataStart;
        public int Kind, Protocol;
        public byte Scl, Sda, Clock, Mosi, Miso, ChipSelect, Value, Read;
        public fixed byte Lines[8];
        public byte LineCount;
        public fixed byte Message[96];
    }
    [StructLayout(LayoutKind.Sequential)] internal struct Device
    {
        public int Protocol;
        public fixed byte Name[48];
        public uint Locator, Identity, MemorySize;
        public byte State, Flags;
        public string GetName() { Device copy=this; byte* p = copy.Name; return Utf8(p,48); }
    }
    [DllImport(Dll, CallingConvention=CallingConvention.Cdecl)] internal static extern IntPtr hpe_gui_sim_create();
    [DllImport(Dll, CallingConvention=CallingConvention.Cdecl)] internal static extern void hpe_gui_sim_destroy(IntPtr sim);
    [DllImport(Dll, CallingConvention=CallingConvention.Cdecl)] internal static extern int hpe_gui_scenario_run(IntPtr sim, [MarshalAs(UnmanagedType.LPUTF8Str)] string text, byte[] error, UIntPtr size);
    [DllImport(Dll, CallingConvention=CallingConvention.Cdecl)] internal static extern UIntPtr hpe_gui_event_count(IntPtr sim);
    [DllImport(Dll, CallingConvention=CallingConvention.Cdecl)] internal static extern int hpe_gui_event_get(IntPtr sim, UIntPtr index, out Event ev);
    [DllImport(Dll, CallingConvention=CallingConvention.Cdecl)] internal static extern UIntPtr hpe_gui_device_count(IntPtr sim);
    [DllImport(Dll, CallingConvention=CallingConvention.Cdecl)] internal static extern int hpe_gui_device_get(IntPtr sim, UIntPtr index, out Device device);
    [DllImport(Dll, CallingConvention=CallingConvention.Cdecl)] internal static extern int hpe_gui_device_read(IntPtr sim, UIntPtr index, uint offset, byte[] data, UIntPtr length);
    internal static string Utf8(byte* p, int n) { int len=0; while(len<n && p[len]!=0) len++; return Encoding.UTF8.GetString(p,len); }
    internal static string Message(Event e) { byte* p=e.Message; return Utf8(p,96); }
    internal static byte Line(Event e, int i) { byte* p=e.Lines; return p[i]; }
}
