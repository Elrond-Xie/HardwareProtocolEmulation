using Avalonia;
using System.Runtime.InteropServices;

namespace Hpe.Gui;

internal static class Program
{
    [STAThread]
    public static int Main(string[] args)
    {
        App.SmokeTest=Array.IndexOf(args,"--smoke-test")>=0;
        args=args.Where(a=>a!="--smoke-test").ToArray();
        if (OperatingSystem.IsWindows())
        {
            string fonts=Path.GetFullPath(Path.Combine(AppContext.BaseDirectory,"..","lib","fonts"));
            if(Directory.Exists(fonts))foreach(string file in Directory.EnumerateFiles(fonts,"*.*"))
                if(Path.GetExtension(file).Equals(".ttf",StringComparison.OrdinalIgnoreCase)||Path.GetExtension(file).Equals(".otf",StringComparison.OrdinalIgnoreCase))
                    AddFontResourceEx(file,0x10,IntPtr.Zero);
        }
        return BuildAvaloniaApp().StartWithClassicDesktopLifetime(args);
    }
    [DllImport("gdi32.dll",CharSet=CharSet.Unicode,EntryPoint="AddFontResourceExW")]
    private static extern int AddFontResourceEx(string name,uint flags,IntPtr reserved);
    public static AppBuilder BuildAvaloniaApp() => AppBuilder.Configure<App>().UsePlatformDetect().LogToTrace();
}
