using System.Collections.ObjectModel;
using System.Globalization;
using Avalonia;
using Avalonia.Controls;
using Avalonia.Controls.Primitives;
using Avalonia.Input;
using Avalonia.Layout;
using Avalonia.Media;
using Avalonia.Platform.Storage;
using Avalonia.Threading;

namespace Hpe.Gui;

public sealed class MainWindow : Window
{
    private readonly ComboBox language = new() { Width=115, SelectedIndex=0 };
    private readonly ComboBox examples = new() { Width=210, SelectedIndex=0 };
    private readonly TextBox scenario = new() { AcceptsReturn=true, FontFamily=new FontFamily("monospace"), MinHeight=120, TextWrapping=TextWrapping.NoWrap };
    private readonly Waveform waveform = new();
    private readonly ListBox trace = new();
    private readonly TextBlock status = new();
    private readonly TextBlock cursorStatus = new();
    private readonly TextBlock versionStatus = new() { Text="HPE 0.1.0" };
    private readonly List<Native.Event> events = new();
    private readonly List<Native.Event> traceEvents = new();
    private readonly List<string> traceRows = new();
    private Button? loadButton,runButton,stateButton,clearButton;
    private MenuItem? fileMenu,settingsMenu,openMenu,saveMenu,languageMenu;
    private TextBlock? scenarioHeader,waveHeaderText;
    private IntPtr sim;
    private string lang="en";
    private long cursorNs;
    private int radix=16;

    public MainWindow()
    {
        Title="HPE"; Width=1180; Height=790; MinWidth=860; MinHeight=600;
        Background=Brushes.White;
        examples.ItemsSource=new[]{"I2C basic","SPI NOR flash","SD / eMMC blocks","USB 2.0 echo","PCIe endpoint"};
        language.ItemsSource=new[]{"English","简体中文"};
        language.SelectionChanged += (_,_)=>{ lang=language.SelectedIndex==1?"zh":"en"; UpdateLanguage(); };
        var root=new DockPanel { LastChildFill=true, Margin=new Thickness(12) };
        var menu=BuildMenu(); DockPanel.SetDock(menu,Dock.Top); root.Children.Add(menu);
        var toolbar=BuildToolbar(); DockPanel.SetDock(toolbar,Dock.Top); root.Children.Add(toolbar);
        var footer=new Border { BorderBrush=Brushes.LightGray, BorderThickness=new Thickness(0,1,0,0), Padding=new Thickness(8,6), Child=new DockPanel() };
        DockPanel.SetDock(footer,Dock.Bottom); root.Children.Add(footer);
        var footPanel=(DockPanel)footer.Child!;
        DockPanel.SetDock(cursorStatus,Dock.Right); footPanel.Children.Add(cursorStatus);DockPanel.SetDock(versionStatus,Dock.Right);footPanel.Children.Add(versionStatus);footPanel.Children.Add(status);
        var split=new Grid { RowDefinitions= new RowDefinitions("Auto,*,Auto,*"), ColumnDefinitions=new ColumnDefinitions("*") };
        var editorLabel=new TextBlock { Text="Scenario", FontWeight=FontWeight.SemiBold, Margin=new Thickness(2,8,0,4) };scenarioHeader=editorLabel;
        split.Children.Add(editorLabel);
        scenario.Text=LoadExampleFile("i2c_basic.hpe"); Grid.SetRow(scenario,1); split.Children.Add(scenario);
        var waveHeader=new Grid { ColumnDefinitions=new ColumnDefinitions("*,Auto,Auto,Auto,Auto"), Margin=new Thickness(0,12,0,4) };
        var waveTitle=new TextBlock { Text="Bus waveform", FontWeight=FontWeight.SemiBold, VerticalAlignment=VerticalAlignment.Center };waveHeaderText=waveTitle;waveHeader.Children.Add(waveTitle);
        var radixBox=new ComboBox { Width=122, ItemsSource=new[]{"Binary","Octal","Decimal","Hex"}, SelectedIndex=3, Margin=new Thickness(4,0) };
        radixBox.SelectionChanged += (_,_)=>{ radix=radixBox.SelectedIndex switch {0=>2,1=>8,2=>10,_=>16}; waveform.Radix=radix; waveform.InvalidateVisual(); };
        Grid.SetColumn(radixBox,1); waveHeader.Children.Add(radixBox);
        foreach(var (label,delta,col) in new[]{("−",-1,2),("+",1,3),("Fit",0,4)}) { var b=new Button{Content=label,MinWidth=40,Margin=new Thickness(3,0)}; b.Click+=(_,_)=>{if(delta==0)waveform.Fit(); else waveform.Zoom(delta);}; Grid.SetColumn(b,col);waveHeader.Children.Add(b); }
        Grid.SetRow(waveHeader,2); split.Children.Add(waveHeader);
        var lower=new Grid { ColumnDefinitions=new ColumnDefinitions("3*,2*"), RowDefinitions=new RowDefinitions("*") };
        waveform.Events=events; waveform.CursorChanged += n=>{cursorNs=n; cursorStatus.Text=$"Cursor {n} ns";};
        lower.Children.Add(new ScrollViewer{Content=waveform,VerticalScrollBarVisibility=ScrollBarVisibility.Auto,HorizontalScrollBarVisibility=ScrollBarVisibility.Disabled});
        trace.ItemsSource=traceRows; trace.SelectionChanged+=(_,_)=>{if(trace.SelectedIndex>=0&&trace.SelectedIndex<traceEvents.Count){cursorNs=(long)traceEvents[trace.SelectedIndex].Time; waveform.CursorNs=cursorNs; waveform.InvalidateVisual();cursorStatus.Text=$"Cursor {cursorNs} ns";}};
        var tracePane=new DockPanel { Margin=new Thickness(8,0,0,0) }; var th=new TextBlock{Text="Decoded events",FontWeight=FontWeight.SemiBold,Margin=new Thickness(0,0,0,4)}; DockPanel.SetDock(th,Dock.Top);tracePane.Children.Add(th);tracePane.Children.Add(trace);Grid.SetColumn(tracePane,1);lower.Children.Add(tracePane);
        Grid.SetRow(lower,3); split.Children.Add(lower); root.Children.Add(split);
        Content=root; UpdateLanguage(); UpdateStatus();
        Opened+=(_,_)=>{if(App.SmokeTest)Dispatcher.UIThread.Post(RunSmokeTest);};
        Closing+=(_,_)=>{ if(sim!=IntPtr.Zero){Native.hpe_gui_sim_destroy(sim);sim=IntPtr.Zero;} };
    }

    private Control BuildMenu()
    {
        var bar=new Menu(); var file=new MenuItem{Header="File"};fileMenu=file;
        var open=new MenuItem{Header="Open…"};openMenu=open; open.Click+=async(_,_)=>await OpenFile();
        var save=new MenuItem{Header="Save…"};saveMenu=save; save.Click+=async(_,_)=>await SaveFile();
        file.Items.Add(open);file.Items.Add(save);bar.Items.Add(file);
        var settings=new MenuItem{Header="Settings"};settingsMenu=settings; var langItem=new MenuItem{Header="Language"};languageMenu=langItem; langItem.Items.Add(language); settings.Items.Add(langItem);
        var add=new MenuItem{Header="＋ Add virtual device…"};add.Click+=async(_,_)=>await AddDevice();settings.Items.Add(add);bar.Items.Add(settings);
        return bar;
    }
    private Control BuildToolbar()
    {
        var p=new StackPanel{Orientation=Orientation.Horizontal,Spacing=7,Margin=new Thickness(0,8,0,8),VerticalAlignment=VerticalAlignment.Center};
        p.Children.Add(examples); loadButton=MakeButton(p,"↧ Load example",LoadSelectedExample);runButton=MakeButton(p,"▶ Run",RunScenario);
        stateButton=MakeButton(p,"▤ Device state",async()=>{await ShowDeviceState();});clearButton=MakeButton(p,"⌫ Clear",Clear);
        return p;
    }
    private static Button MakeButton(Panel p,string text,Action action){var b=new Button{Content=text,Padding=new Thickness(11,5)};b.Click+=(_,_)=>action();p.Children.Add(b);return b;}
    private void UpdateLanguage()
    {
        bool zh=lang=="zh";Title="HPE";scenario.PlaceholderText=zh?"输入或载入协议场景…":"Enter or load a protocol scenario…";
        int selectedExample=Math.Max(0,examples.SelectedIndex);examples.ItemsSource=zh?new[]{"I2C 基础","SPI NOR 闪存","SD / eMMC 块设备","USB 2.0 回环","PCIe 端点"}:new[]{"I2C basic","SPI NOR flash","SD / eMMC blocks","USB 2.0 echo","PCIe endpoint"};examples.SelectedIndex=selectedExample;
        if(fileMenu!=null)fileMenu.Header=zh?"文件":"File";if(settingsMenu!=null)settingsMenu.Header=zh?"设置":"Settings";
        if(openMenu!=null)openMenu.Header=zh?"打开…":"Open…";if(saveMenu!=null)saveMenu.Header=zh?"保存…":"Save…";if(languageMenu!=null)languageMenu.Header=zh?"界面语言":"Language";
        if(stateButton!=null)stateButton.Content=zh?"▤ 设备状态":"▤ Device state";if(clearButton!=null)clearButton.Content=zh?"⌫ 清理":"⌫ Clear";
        if(loadButton!=null)loadButton.Content=zh?"↧ 载入示例":"↧ Load example";if(runButton!=null)runButton.Content=zh?"▶ 运行":"▶ Run";
        if(scenarioHeader!=null)scenarioHeader.Text=zh?"场景":"Scenario";if(waveHeaderText!=null)waveHeaderText.Text=zh?"总线时序":"Bus waveform";
        UpdateStatus();
    }
    private void UpdateStatus()
    {
        int protocol=events.Count==0?0:events[^1].Protocol;
        string pn=protocol switch{1=>"I2C",2=>"SPI",3=>"SD",4=>"eMMC",5=>"USB",6=>"PCIe",_=>lang=="zh"?"未运行":"idle"};
        int count=sim==IntPtr.Zero?0:(int)Native.hpe_gui_device_count(sim).ToUInt64();
        status.Text=lang=="zh"?$"协议：{pn}　|　虚拟设备：{count}":$"Protocol: {pn}  |  Virtual devices: {count}";
    }
    private string LoadExampleFile(string name){var path=Path.Combine(AppContext.BaseDirectory,"examples",name);return File.Exists(path)?File.ReadAllText(path):"";}
    private void LoadSelectedExample(){var files=new[]{"i2c/i2c_basic.hpe","spi/spi_nor.hpe","sdmmc/sd_emmc_blocks.hpe","usb/usb2_echo.hpe","pcie/pcie_basic.hpe"};scenario.Text=LoadExampleFile(files[Math.Clamp(examples.SelectedIndex,0,files.Length-1)]);}
    private void RunScenario()
    {
        if(sim!=IntPtr.Zero) Native.hpe_gui_sim_destroy(sim); sim=Native.hpe_gui_sim_create();
        if(sim==IntPtr.Zero){ShowError("Could not create simulator");return;}
        var err=new byte[512]; int result=Native.hpe_gui_scenario_run(sim,scenario.Text??"",err,(UIntPtr)err.Length);
        if(result!=0){var msg=System.Text.Encoding.UTF8.GetString(err).TrimEnd('\0');ShowError(msg);}
        events.Clear();traceRows.Clear();traceEvents.Clear(); var n=(int)Native.hpe_gui_event_count(sim).ToUInt64();
        for(int i=0;i<n;i++){Native.hpe_gui_event_get(sim,(UIntPtr)i,out var e);events.Add(e);if(e.Kind!=0){traceEvents.Add(e);traceRows.Add($"{e.Time,10} ns  {Kind(e.Kind),-10} {Native.Message(e)}");}}
        trace.ItemsSource=traceRows.ToArray();waveform.Events=events;waveform.CursorNs=events.Count==0?0:(long)events[0].Time;waveform.Fit();UpdateStatus();
    }
    private static string Kind(int k)=>k switch{1=>"START",2=>"RESTART",3=>"STOP",4=>"ADDRESS",5=>"DATA",6=>"ACK",7=>"NACK",10=>"COMMAND",11=>"RESPONSE",12=>"BLOCK",_=>"INFO"};
    private void Clear(){if(sim!=IntPtr.Zero){Native.hpe_gui_sim_destroy(sim);sim=IntPtr.Zero;}events.Clear();traceRows.Clear();traceEvents.Clear();trace.ItemsSource=Array.Empty<string>();waveform.Events=events;waveform.InvalidateVisual();scenario.Clear();UpdateStatus();}
    private async Task AddDevice()
    {
        var name=new TextBox{Name="deviceName",Text="sensor",PlaceholderText="Name"};var addr=new TextBox{Name="deviceAddress",Text="0x48",Width=110};var size=new NumericUpDown{Name="deviceSize",Minimum=1,Maximum=65536,Value=256};var fill=new TextBox{Name="deviceFill",Text="0x00",Width=110};
        var form=new StackPanel{Spacing=8,Margin=new Thickness(18)};form.Children.Add(new TextBlock{Text=lang=="zh"?"添加虚拟 I2C 从设备":"Add virtual I2C device",FontSize=18});form.Children.Add(new TextBlock{Text="Name"});form.Children.Add(name);form.Children.Add(new TextBlock{Text="7-bit address"});form.Children.Add(addr);form.Children.Add(new TextBlock{Text="Register bytes"});form.Children.Add(size);form.Children.Add(new TextBlock{Text="Initial fill (hex)"});form.Children.Add(fill);
        var buttons=new StackPanel{Orientation=Orientation.Horizontal,HorizontalAlignment=HorizontalAlignment.Right,Spacing=8};var cancel=new Button{Content="Cancel"};var ok=new Button{Content="Add"};buttons.Children.Add(cancel);buttons.Children.Add(ok);form.Children.Add(buttons);
        var dlg=new Window{Title="Add virtual device",Width=350,Height=430,WindowStartupLocation=WindowStartupLocation.CenterOwner,Content=form};cancel.Click+=(_,_)=>dlg.Close();ok.Click+=(_,_)=>{try{int a=Convert.ToInt32(addr.Text,16),f=Convert.ToInt32(fill.Text,16);if(a>127||f>255)throw new FormatException();var text=$"\ndevice {name.Text} 0x{a:X2} {(int)size.Value!} 0x{f:X2}\n";scenario.Text+=text;dlg.Close();}catch{ShowError("Address and fill must be valid hexadecimal values.");}};
        if(App.SmokeTest)dlg.Opened+=(_,_)=>{name.Text="gui_dialog_smoke";addr.Text="0x49";size.Value=32;fill.Text="0xA5";ok.RaiseEvent(new Avalonia.Interactivity.RoutedEventArgs(Button.ClickEvent));};
        await dlg.ShowDialog(this);
    }
    private async Task<bool> ShowDeviceState()
    {
        if(sim==IntPtr.Zero||Native.hpe_gui_device_count(sim).ToUInt64()==0){ShowError("Run a scenario containing virtual devices first.");return false;}
        var list=new ListBox();var dump=new TextBox{IsReadOnly=true,FontFamily=new FontFamily("monospace"),AcceptsReturn=true,TextWrapping=TextWrapping.Wrap};
        var panel=new Grid{ColumnDefinitions=new ColumnDefinitions("220,*"),Margin=new Thickness(12)};panel.Children.Add(list);Grid.SetColumn(dump,1);panel.Children.Add(dump);
        var win=new Window{Title="Device state",Width=740,Height=500,Content=panel,WindowStartupLocation=WindowStartupLocation.CenterOwner};
        var n=(int)Native.hpe_gui_device_count(sim).ToUInt64();var devices=new List<Native.Device>();for(int i=0;i<n;i++){Native.hpe_gui_device_get(sim,(UIntPtr)i,out var d);devices.Add(d);list.Items.Add($"{d.GetName()}  [{ProtocolName(d.Protocol)}]");}
        int selected=-1;
        void RefreshMemory(){int i=selected;if(i<0||sim==IntPtr.Zero)return;var d=devices[i];var bytes=new byte[Math.Min(512,(int)d.MemorySize)];int rc=Native.hpe_gui_device_read(sim,(UIntPtr)i,0,bytes,(UIntPtr)bytes.Length);dump.Text=rc==0?FormatMemory(bytes):$"Unable to read device memory (status {rc}).";}
        list.SelectionChanged+=(_,_)=>{selected=list.SelectedIndex;RefreshMemory();};
        var refresh=new DispatcherTimer{Interval=TimeSpan.FromMilliseconds(350)};refresh.Tick+=(_,_)=>RefreshMemory();refresh.Start();win.Closed+=(_,_)=>refresh.Stop();
        if(App.SmokeTest){var closeTimer=new DispatcherTimer{Interval=TimeSpan.FromMilliseconds(250)};closeTimer.Tick+=(_,_)=>{closeTimer.Stop();win.Close();};win.Opened+=(_,_)=>closeTimer.Start();}
        if(n>0)list.SelectedIndex=0;await win.ShowDialog(this);return (dump.Text??"").StartsWith("Offset",StringComparison.Ordinal);
    }
    private static string ProtocolName(int p)=>p switch{1=>"I2C",2=>"SPI",3=>"SD",4=>"eMMC",5=>"USB",6=>"PCIe",_=>"Unknown"};
    private static string FormatMemory(byte[] bytes){var sb=new System.Text.StringBuilder("Offset   00 01 02 03 04 05 06 07 08 09 0A 0B 0C 0D 0E 0F\n");for(int i=0;i<bytes.Length;i+=16){sb.Append($"{i:X6}   ");for(int j=0;j<16;j++)sb.Append(i+j<bytes.Length?$"{bytes[i+j]:X2} ":"   ");sb.AppendLine();}return sb.ToString();}
    private void ShowError(string message){var w=new Window{Title="HPE",Width=460,Height=180,WindowStartupLocation=WindowStartupLocation.CenterOwner,Content=new StackPanel{Margin=new Thickness(18),Spacing=12,Children={new TextBlock{Text=message,TextWrapping=TextWrapping.Wrap},new Button{Content="OK",HorizontalAlignment=HorizontalAlignment.Right}}}};((Button)((StackPanel)w.Content!).Children[1]).Click+=(_,_)=>w.Close();w.ShowDialog(this);}
    private async Task OpenFile(){var files=await StorageProvider.OpenFilePickerAsync(new FilePickerOpenOptions{AllowMultiple=false,FileTypeFilter=new[]{new FilePickerFileType("HPE scenario"){Patterns=new[]{"*.hpe"}}}});if(files.Count>0)scenario.Text=await File.ReadAllTextAsync(files[0].Path.LocalPath);}
    private async Task SaveFile(){var f=await StorageProvider.SaveFilePickerAsync(new FilePickerSaveOptions{SuggestedFileName="scenario.hpe",FileTypeChoices=new[]{new FilePickerFileType("HPE scenario"){Patterns=new[]{"*.hpe"}}}});if(f!=null)await File.WriteAllTextAsync(f.Path.LocalPath,scenario.Text);}
    private async void RunSmokeTest()
    {
        var report=new List<string>();int failures=0;
        void Record(string value){report.Add(value);File.WriteAllLines(Path.Combine(Path.GetTempPath(),"hpe-gui-smoke-test.log"),report);}
        void Check(bool ok,string name){report.Add($"{(ok?"PASS":"FAIL")} {name}");if(!ok)failures++;}
        try
        {
            string[] files={"i2c_basic.hpe","spi_nor.hpe","sd_emmc_blocks.hpe","usb2_echo.hpe","pcie_basic.hpe"};
            int[] protocols={1,2,3,5,6};
            Record("SMOKE START");
            for(int i=0;i<files.Length;i++){Record($"START {files[i]}");scenario.Text=LoadExampleFile(files[i]);Record($"LOADED {files[i]}");RunScenario();Record($"RAN {files[i]} events={events.Count}");Check(events.Count>0,$"run {files[i]}");Check(events.Count==0||events[^1].Protocol==protocols[i],$"protocol trace {files[i]}");Check(sim!=IntPtr.Zero&&Native.hpe_gui_device_count(sim).ToUInt64()>0,$"virtual device {files[i]}");}
            scenario.Text=LoadExampleFile(files[0]);RunScenario();ulong before=Native.hpe_gui_device_count(sim).ToUInt64();await AddDevice();RunScenario();
            Check(Native.hpe_gui_device_count(sim).ToUInt64()==before+1,"add virtual device");
            Check(Native.hpe_gui_device_get(sim,(UIntPtr)before,out var device)==0&&device.GetName()=="gui_dialog_smoke","add device dialog and inspect state");
            Check(Native.hpe_gui_device_read(sim,(UIntPtr)before,0,new byte[16],(UIntPtr)16)==0,"read device memory");
            Check(await ShowDeviceState(),"device state window and live memory view");
            language.SelectedIndex=1;Check(lang=="zh","switch Simplified Chinese");language.SelectedIndex=0;
            waveform.Radix=2;waveform.Zoom(1);waveform.Fit();int row=Math.Min(2,traceEvents.Count-1);trace.SelectedIndex=row;
            Check(row>=0&&waveform.CursorNs==(long)traceEvents[row].Time,"trace selection follows waveform cursor");
            Clear();Check(events.Count==0&&sim==IntPtr.Zero,"clear simulator and trace");
        }
        catch(Exception e){report.Add("FAIL exception: "+e);failures++;}
        File.WriteAllLines(Path.Combine(Path.GetTempPath(),"hpe-gui-smoke-test.log"),report);
        var lifetime=(Avalonia.Controls.ApplicationLifetimes.IClassicDesktopStyleApplicationLifetime)App.Current!.ApplicationLifetime!;
        lifetime.Shutdown(failures==0?0:1);
    }
}

internal sealed class Waveform : Control
{
    public IReadOnlyList<Native.Event> Events {get;set;}=Array.Empty<Native.Event>();
    public long CursorNs {get;set;}
    public int Radix {get;set;}=16;
    public Action<long>? CursorChanged {get;set;}
    private double scale=0.025, offset;
    private Point? pan;
    private static readonly IBrush[] Colors={Brush.Parse("#007AFF"),Brush.Parse("#34C759"),Brush.Parse("#FF9500"),Brush.Parse("#AF52DE"),Brush.Parse("#FF3B30"),Brush.Parse("#5AC8FA"),Brush.Parse("#5856D6"),Brush.Parse("#8E8E93"),Brush.Parse("#00A6A6"),Brush.Parse("#C44569")};
    public Waveform(){ClipToBounds=true;MinHeight=410;PointerPressed+=Down;PointerMoved+=Move;PointerReleased+=Up;PointerWheelChanged+=Wheel;}
    public override void Render(DrawingContext c)
    {
        base.Render(c);var b=Bounds;c.FillRectangle(Brushes.White,b);c.DrawRectangle(null,new Pen(Brush.Parse("#D9DDE3")),b);
        var ch=Channels();var dataEvents=Events.Where(e=>e.Kind is 5 or 10 or 11).ToArray();double top=26,row=38;using(c.PushClip(new Rect(0,0,b.Width,b.Height))){
            for(int x=78;x<b.Width;x+=100){double t=(x-78+offset)/scale;c.DrawLine(new Pen(Brush.Parse("#E8EBEF"),1),new Point(x,0),new Point(x,b.Height));c.DrawText(new FormattedText($"{t:0}",CultureInfo.InvariantCulture,FlowDirection.LeftToRight,Typeface.Default,11,Brushes.Gray),new Point(x+2,4));}
            for(int i=0;i<ch.Count;i++)
            {
                double y=top+i*row;var pen=new Pen(Colors[i%Colors.Length],2);
                c.DrawText(new FormattedText(ch[i],CultureInfo.InvariantCulture,FlowDirection.LeftToRight,Typeface.Default,12,Brushes.DarkSlateGray),new Point(5,y+4));
                c.DrawLine(new Pen(Brush.Parse("#EFF1F4")),new Point(75,y+row-2),new Point(b.Width,y+row-2));
                bool? last=null;double lastX=78;
                foreach(var e in Events)
                {
                    bool? value=Signal(e,ch[i]);if(value is null||value==last)continue;
                    double x=78+(long)e.Time*scale-offset;if(x<78){last=value;lastX=x;continue;}if(x>b.Width)break;
                    if(last.HasValue){double yy=last.Value?y+8:y+25;c.DrawLine(pen,new Point(lastX,yy),new Point(x,yy));c.DrawLine(pen,new Point(x,y+8),new Point(x,y+25));}
                    last=value;lastX=x;
                }
                if(last.HasValue)c.DrawLine(pen,new Point(lastX,last.Value?y+8:y+25),new Point(b.Width,last.Value?y+8:y+25));
                if(ch[i] is "SDA" or "MOSI" or "MISO" or "CMD" or "DAT0")
                {
                    double lastLabelX=-100;
                    foreach(var e in dataEvents){double x=78+(long)e.Time*scale-offset;if(x<78||x+42>b.Width||x-lastLabelX<44)continue;string value=Radix switch{2=>Convert.ToString(e.Value,2).PadLeft(8,'0'),8=>Convert.ToString(e.Value,8),10=>e.Value.ToString(CultureInfo.InvariantCulture),_=>e.Value.ToString("X2")};c.DrawText(new FormattedText(value,CultureInfo.InvariantCulture,FlowDirection.LeftToRight,Typeface.Default,10,Brushes.Black),new Point(x+2,y-3));lastLabelX=x;}
                }
            }
            if(CursorNs>=0){double x=78+CursorNs*scale-offset;if(x>=78&&x<=b.Width)c.DrawLine(new Pen(Brushes.LimeGreen,2),new Point(x,0),new Point(x,b.Height));}
        }
    }
    private List<string> Channels(){if(Events.Count==0)return new(){"SCL / CLK","SDA / MOSI","MISO / DAT0"};int p=Events[^1].Protocol;return p switch{1=>new(){"SCL","SDA"},2=>new(){"SCLK","MOSI","MISO","CS"},3 or 4=>new(){"CLK","CMD","DAT0","DAT1","DAT2","DAT3","DAT4","DAT5","DAT6","DAT7"},5=>new(){"USB D+","USB D−"},6=>new(){"REFCLK","TX","RX","LTSSM"},_=>new(){"CLK","DATA"}};}
    private static bool? Signal(Native.Event e,string ch)=>ch switch{"SCL"=>e.Scl!=0,"SDA"=>e.Sda!=0,"SCLK"=>e.Clock!=0,"MOSI"=>e.Mosi!=0,"MISO"=>e.Miso!=0,"CS"=>e.ChipSelect==0,"CLK"=>e.Clock!=0,"CMD"=>e.Mosi!=0,"DAT0"=>Native.Line(e,0)!=0,"DAT1"=>Native.Line(e,1)!=0,"DAT2"=>Native.Line(e,2)!=0,"DAT3"=>Native.Line(e,3)!=0,"DAT4"=>Native.Line(e,4)!=0,"DAT5"=>Native.Line(e,5)!=0,"DAT6"=>Native.Line(e,6)!=0,"DAT7"=>Native.Line(e,7)!=0,"USB D+"=>e.Mosi!=0,"USB D−"=>e.Miso!=0,"TX"=>e.Mosi!=0,"RX"=>e.Miso!=0,"REFCLK"=>e.Clock!=0,"LTSSM"=>e.Value%2==1,_=>null};
    public void Zoom(int d,double? anchorX=null){double x=anchorX??Bounds.Width/2;double time=(x-78+offset)/scale;scale=Math.Clamp(scale*(d>0?1.35:1/1.35),0.002,3);offset=Math.Max(0,time*scale-(x-78));InvalidateVisual();}
    public void Fit(){offset=0;if(Events.Count>0){long max=(long)Events[^1].Time;scale=Math.Clamp((Bounds.Width-100)/Math.Max(1,max),0.002,3);}InvalidateVisual();}
    private void Wheel(object? s,PointerWheelEventArgs e){Zoom(e.Delta.Y>0?1:-1,e.GetPosition(this).X);e.Handled=true;}
    private void Down(object? s,PointerPressedEventArgs e){var p=e.GetPosition(this);if(e.GetCurrentPoint(this).Properties.IsMiddleButtonPressed){pan=p;e.Pointer.Capture(this);}else if(p.X>=78){CursorNs=(long)Math.Max(0,(p.X-78+offset)/scale);CursorChanged?.Invoke(CursorNs);InvalidateVisual();}}
    private void Move(object? s,PointerEventArgs e){if(pan is Point p){var now=e.GetPosition(this);offset=Math.Max(0,offset-(now.X-p.X));pan=now;InvalidateVisual();}}
    private void Up(object? s,PointerReleasedEventArgs e){pan=null;e.Pointer.Capture(null);}
}
