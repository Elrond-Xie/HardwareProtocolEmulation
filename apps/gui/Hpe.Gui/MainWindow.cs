// File: MainWindow.cs
// Purpose: Project-owned GUI component for apps / gui / Hpe.Gui / MainWindow.cs.
// Author: Elrond-Xie
// Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.

using System.Globalization;
using Avalonia;
using Avalonia.Controls;
using Avalonia.Controls.Primitives;
using Avalonia.Input;
using Avalonia.Layout;
using Avalonia.Media;
using Avalonia.Platform.Storage;
using Avalonia.Threading;

namespace hpe.gui;
/// <summary>Represents the main window type.</summary>
public sealed class main_window : Window
{
    private readonly ComboBox language = new() { Width = 115, SelectedIndex = 0 };
    private readonly ComboBox examples = new() { Width = 210, SelectedIndex = 0 };
    private readonly TextBox scenario =
        new() { AcceptsReturn = true, FontFamily = new FontFamily("monospace"), MinHeight = 120,
                TextWrapping = TextWrapping.NoWrap };
    private readonly waveform_view waveform = new();
    private readonly ListBox trace = new();
    private readonly TextBlock status = new();
    private readonly TextBlock cursor_status = new();
    private readonly TextBlock version_status = new() { Text = "HPE 0.1.0" };
    private readonly List<native_api.native_event> events = new();
    private readonly List<native_api.native_event> trace_events = new();
    private readonly List<string> trace_rows = new();
    private Button? load_button, run_button, state_button, clear_button;
    private MenuItem? file_menu, settings_menu, open_menu, save_menu, language_menu;
    private TextBlock? scenario_header, wave_header_text;
    private IntPtr sim;
    private string lang = "en";
    private long cursor_ns;
    private int radix = 16;
    /// <summary>Performs the main window operation.</summary>
    public main_window()
    {
        Title = "HPE";
        Width = 1180;
        Height = 790;
        MinWidth = 860;
        MinHeight = 600;
        Background = Brushes.White;
        examples.ItemsSource = new[] { "I2C basic", "SPI NOR flash", "SD / eMMC blocks",
                                       "USB 2.0 echo", "PCIe endpoint" };
        language.ItemsSource = new[] { "English", "简体中文" };
        language.SelectionChanged += (_, _) =>
        {
            lang = language.SelectedIndex == 1 ? "zh" : "en";
            update_language();
        };
        var root = new DockPanel { LastChildFill = true, Margin = new Thickness(12) };
        var menu = build_menu();
        DockPanel.SetDock(menu, Dock.Top);
        root.Children.Add(menu);
        var toolbar = build_toolbar();
        DockPanel.SetDock(toolbar, Dock.Top);
        root.Children.Add(toolbar);
        var footer = new Border { BorderBrush = Brushes.LightGray,
                                  BorderThickness = new Thickness(0, 1, 0, 0),
                                  Padding = new Thickness(8, 6), Child = new DockPanel() };
        DockPanel.SetDock(footer, Dock.Bottom);
        root.Children.Add(footer);
        var foot_panel = (DockPanel)footer.Child!;
        DockPanel.SetDock(cursor_status, Dock.Right);
        foot_panel.Children.Add(cursor_status);
        DockPanel.SetDock(version_status, Dock.Right);
        foot_panel.Children.Add(version_status);
        foot_panel.Children.Add(status);
        var split = new Grid { RowDefinitions = new RowDefinitions("Auto,*,Auto,*"),
                               ColumnDefinitions = new ColumnDefinitions("*") };
        var editor_label = new TextBlock { Text = "Scenario", FontWeight = FontWeight.SemiBold,
                                           Margin = new Thickness(2, 8, 0, 4) };
        scenario_header = editor_label;
        split.Children.Add(editor_label);
        scenario.Text = load_example_file("i2c/i2c_basic.hpe");
        Grid.SetRow(scenario, 1);
        split.Children.Add(scenario);
        var wave_header =
            new Grid { ColumnDefinitions = new ColumnDefinitions("*,Auto,Auto,Auto,Auto"),
                       Margin = new Thickness(0, 12, 0, 4) };
        var wave_title = new TextBlock { Text = "Bus waveform", FontWeight = FontWeight.SemiBold,
                                         VerticalAlignment = VerticalAlignment.Center };
        wave_header_text = wave_title;
        wave_header.Children.Add(wave_title);
        var radix_box =
            new ComboBox { Width = 122, ItemsSource = new[] { "Binary", "Octal", "Decimal", "Hex" },
                           SelectedIndex = 3, Margin = new Thickness(4, 0) };
        radix_box.SelectionChanged += (_, _) =>
        {
            radix = radix_box.SelectedIndex switch {
                0 => 2,
                1 => 8,
                2 => 10,
                _ => 16
            };
            waveform.radix = radix;
            waveform.InvalidateVisual();
        };
        Grid.SetColumn(radix_box, 1);
        wave_header.Children.Add(radix_box);
        foreach (var (label, delta, col) in new[] { ("−", -1, 2), ("+", 1, 3), ("fit", 0, 4) }) {
            var b = new Button { Content = label, MinWidth = 40, Margin = new Thickness(3, 0) };
            b.Click += (_, _) =>
            {
                if (delta == 0) {
                    waveform.fit();
                }
                else {
                    waveform.zoom(delta);
                }
            };
            Grid.SetColumn(b, col);
            wave_header.Children.Add(b);
        }
        Grid.SetRow(wave_header, 2);
        split.Children.Add(wave_header);
        var lower = new Grid { ColumnDefinitions = new ColumnDefinitions("3*,2*"),
                               RowDefinitions = new RowDefinitions("*") };
        waveform.events = events;
        waveform.cursor_changed += n =>
        {
            cursor_ns = n;
            cursor_status.Text = $"Cursor {n} ns";
        };
        lower.Children.Add(
            new ScrollViewer { Content = waveform,
                               VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
                               HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled });
        trace.ItemsSource = trace_rows;
        trace.SelectionChanged += (_, _) =>
        {
            if (trace.SelectedIndex >= 0 && trace.SelectedIndex < trace_events.Count) {
                cursor_ns = (long)trace_events[trace.SelectedIndex].time;
                waveform.cursor_ns = cursor_ns;
                waveform.InvalidateVisual();
                cursor_status.Text = $"Cursor {cursor_ns} ns";
            }
        };
        var trace_pane = new DockPanel { Margin = new Thickness(8, 0, 0, 0) };
        var th = new TextBlock { Text = "Decoded events", FontWeight = FontWeight.SemiBold,
                                 Margin = new Thickness(0, 0, 0, 4) };
        DockPanel.SetDock(th, Dock.Top);
        trace_pane.Children.Add(th);
        trace_pane.Children.Add(trace);
        Grid.SetColumn(trace_pane, 1);
        lower.Children.Add(trace_pane);
        Grid.SetRow(lower, 3);
        split.Children.Add(lower);
        root.Children.Add(split);
        Content = root;
        update_language();
        update_status();
        Opened += (_, _) =>
        {
            if (hpe_app.smoke_test) {
                Dispatcher.UIThread.Post(run_smoke_test);
            }
        };
        Closing += (_, _) =>
        {
            if (sim != IntPtr.Zero) {
                native_api.hpe_gui_sim_destroy(sim);
                sim = IntPtr.Zero;
            }
        };
    }
    /// <summary>Performs the build menu operation.</summary>
    private Control build_menu()
    {
        var bar = new Menu();
        var file = new MenuItem { Header = "File" };
        file_menu = file;
        var open = new MenuItem { Header = "Open…" };
        open_menu = open;
        open.Click += async (_, _) => await open_file();
        var save = new MenuItem { Header = "Save…" };
        save_menu = save;
        save.Click += async (_, _) => await save_file();
        file.Items.Add(open);
        file.Items.Add(save);
        bar.Items.Add(file);
        var settings = new MenuItem { Header = "Settings" };
        settings_menu = settings;
        var lang_item = new MenuItem { Header = "Language" };
        language_menu = lang_item;
        lang_item.Items.Add(language);
        settings.Items.Add(lang_item);
        var add = new MenuItem { Header = "＋ Add virtual device…" };
        add.Click += async (_, _) => await add_device();
        settings.Items.Add(add);
        bar.Items.Add(settings);
        return bar;
    }
    /// <summary>Performs the build toolbar operation.</summary>
    private Control build_toolbar()
    {
        var p = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 7,
                                 Margin = new Thickness(0, 8, 0, 8),
                                 VerticalAlignment = VerticalAlignment.Center };
        p.Children.Add(examples);
        load_button = make_button(p, "↧ Load example", load_selected_example);
        run_button = make_button(p, "▶ Run", run_scenario);
        state_button = make_button(p, "▤ Device state",
                                   async () =>
                                   { await show_device_state(); });
        clear_button = make_button(p, "⌫ Clear", clear_simulation);
        return p;
    }
    /// <summary>Performs the make button operation.</summary>
    private static Button make_button(Panel p, string text, Action action)
    {
        var b = new Button { Content = text, Padding = new Thickness(11, 5) };
        b.Click += (_, _) => action();
        p.Children.Add(b);
        return b;
    }
    /// <summary>Performs the update language operation.</summary>
    private void update_language()
    {
        bool zh = lang == "zh";
        Title = "HPE";
        scenario.PlaceholderText =
            zh ? "输入或载入协议场景…" : "Enter or load a protocol scenario…";
        int selected_example = Math.Max(0, examples.SelectedIndex);
        examples.ItemsSource = zh ? new[] { "I2C 基础", "SPI NOR 闪存", "SD / eMMC 块设备",
                                            "USB 2.0 回环", "PCIe 端点" }
                                  : new[] { "I2C basic", "SPI NOR flash", "SD / eMMC blocks",
                                            "USB 2.0 echo", "PCIe endpoint" };
        examples.SelectedIndex = selected_example;
        if (file_menu != null) {
            file_menu.Header = zh ? "文件" : "File";
        }
        if (settings_menu != null) {
            settings_menu.Header = zh ? "设置" : "Settings";
        }
        if (open_menu != null) {
            open_menu.Header = zh ? "打开…" : "Open…";
        }
        if (save_menu != null) {
            save_menu.Header = zh ? "保存…" : "Save…";
        }
        if (language_menu != null) {
            language_menu.Header = zh ? "界面语言" : "Language";
        }
        if (state_button != null) {
            state_button.Content = zh ? "▤ 设备状态" : "▤ Device state";
        }
        if (clear_button != null) {
            clear_button.Content = zh ? "⌫ 清理" : "⌫ Clear";
        }
        if (load_button != null) {
            load_button.Content = zh ? "↧ 载入示例" : "↧ Load example";
        }
        if (run_button != null) {
            run_button.Content = zh ? "▶ 运行" : "▶ Run";
        }
        if (scenario_header != null) {
            scenario_header.Text = zh ? "场景" : "Scenario";
        }
        if (wave_header_text != null) {
            wave_header_text.Text = zh ? "总线时序" : "Bus waveform";
        }
        update_status();
    }
    /// <summary>Performs the update status operation.</summary>
    private void update_status()
    {
        int protocol = events.Count == 0 ? 0 : events[^1].protocol;
        string pn = protocol switch {
            1 => "I2C",
            2 => "SPI",
            3 => "SD",
            4 => "eMMC",
            5 => "USB",
            6 => "PCIe",
            _ => lang == "zh" ? "未运行" : "idle"
        };
        int count = sim == IntPtr.Zero ? 0 : (int)native_api.hpe_gui_device_count(sim).ToUInt64();
        status.Text = lang == "zh" ? $"协议：{pn}　|　虚拟设备：{count}"
                                   : $"protocol: {pn}  |  Virtual devices: {count}";
    }
    /// <summary>Performs the load example file operation.</summary>
    private string load_example_file(string name)
    {
        var path = Path.Combine(AppContext.BaseDirectory, "examples", name);
        return File.Exists(path) ? File.ReadAllText(path) : "";
    }
    /// <summary>Performs the load selected example operation.</summary>
    private void load_selected_example()
    {
        var files = new[] { "i2c/i2c_basic.hpe", "spi/spi_nor.hpe", "sdmmc/sd_emmc_blocks.hpe",
                            "usb/usb2_echo.hpe", "pcie/pcie_basic.hpe" };
        scenario.Text =
            load_example_file(files[Math.Clamp(examples.SelectedIndex, 0, files.Length - 1)]);
    }
    /// <summary>Performs the run scenario operation.</summary>
    private void run_scenario()
    {
        if (sim != IntPtr.Zero) {
            native_api.hpe_gui_sim_destroy(sim);
        }
        sim = native_api.hpe_gui_sim_create();
        if (sim == IntPtr.Zero) {
            show_error("Could not create simulator");
            return;
        }
        var err = new byte[512];
        int result =
            native_api.hpe_gui_scenario_run(sim, scenario.Text ?? "", err, (UIntPtr)err.Length);
        if (result != 0) {
            var msg = System.Text.Encoding.UTF8.GetString(err).TrimEnd('\0');
            show_error(msg);
        }
        events.Clear();
        trace_rows.Clear();
        trace_events.Clear();
        var n = (int)native_api.hpe_gui_event_count(sim).ToUInt64();
        for (int i = 0; i < n; i++) {
            native_api.hpe_gui_event_get(sim, (UIntPtr)i, out var e);
            events.Add(e);
            if (e.kind != 0) {
                trace_events.Add(e);
                trace_rows.Add($"{e.time,10} ns  {kind(e.kind),-10} {native_api.message(e)}");
            }
        }
        trace.ItemsSource = trace_rows.ToArray();
        waveform.events = events;
        waveform.cursor_ns = events.Count == 0 ? 0 : (long)events[0].time;
        waveform.fit();
        update_status();
    }
    /// <summary>Performs the kind operation.</summary>
    private static string kind(int k) => k switch {
        1 => "START",
        2 => "RESTART",
        3 => "STOP",
        4 => "ADDRESS",
        5 => "DATA",
        6 => "ACK",
        7 => "NACK",
        10 => "COMMAND",
        11 => "RESPONSE",
        12 => "BLOCK",
        _ => "INFO"
    };
    /// <summary>Performs the clear simulation operation.</summary>
    private void clear_simulation()
    {
        if (sim != IntPtr.Zero) {
            native_api.hpe_gui_sim_destroy(sim);
            sim = IntPtr.Zero;
        }
        events.Clear();
        trace_rows.Clear();
        trace_events.Clear();
        trace.ItemsSource = Array.Empty<string>();
        waveform.events = events;
        waveform.InvalidateVisual();
        scenario.Clear();
        update_status();
    }
    /// <summary>Performs the add device operation.</summary>
    private async Task add_device()
    {
        var name = new TextBox { Name = "device_name", Text = "sensor", PlaceholderText = "Name" };
        var addr = new TextBox { Name = "device_address", Text = "0x48", Width = 110 };
        var size =
            new NumericUpDown { Name = "device_size", Minimum = 1, Maximum = 65536, Value = 256 };
        var fill = new TextBox { Name = "device_fill", Text = "0x00", Width = 110 };
        var form = new StackPanel { Spacing = 8, Margin = new Thickness(18) };
        form.Children.Add(
            new TextBlock { Text = lang == "zh" ? "添加虚拟 I2C 从设备" : "Add virtual I2C device",
                            FontSize = 18 });
        form.Children.Add(new TextBlock { Text = "Name" });
        form.Children.Add(name);
        form.Children.Add(new TextBlock { Text = "7-bit address" });
        form.Children.Add(addr);
        form.Children.Add(new TextBlock { Text = "Register bytes" });
        form.Children.Add(size);
        form.Children.Add(new TextBlock { Text = "Initial fill (hex)" });
        form.Children.Add(fill);
        var buttons =
            new StackPanel { Orientation = Orientation.Horizontal,
                             HorizontalAlignment = HorizontalAlignment.Right, Spacing = 8 };
        var cancel = new Button { Content = "Cancel" };
        var ok = new Button { Content = "Add" };
        buttons.Children.Add(cancel);
        buttons.Children.Add(ok);
        form.Children.Add(buttons);
        var dlg = new Window { Title = "Add virtual device", Width = 350, Height = 430,
                               WindowStartupLocation = WindowStartupLocation.CenterOwner,
                               Content = form };
        cancel.Click += (_, _) => dlg.Close();
        ok.Click += (_, _) =>
        {
            try {
                int a = Convert.ToInt32(addr.Text, 16), f = Convert.ToInt32(fill.Text, 16);
                if (a > 127 || f > 255) {
                    throw new FormatException();
                }
                var text = $"\ndevice {name.Text} 0x{a:X2} {(int)size.Value!} 0x{f:X2}\n";
                scenario.Text += text;
                dlg.Close();
            } catch {
                show_error("Address and fill must be valid hexadecimal values.");
            }
        };
        if (hpe_app.smoke_test) {
            dlg.Opened += (_, _) =>
            {
                name.Text = "gui_dialog_smoke";
                addr.Text = "0x49";
                size.Value = 32;
                fill.Text = "0xA5";
                ok.RaiseEvent(new Avalonia.Interactivity.RoutedEventArgs(Button.ClickEvent));
            };
        }
        await dlg.ShowDialog(this);
    }
    /// <summary>Performs the show device state operation.</summary>
    private async Task<bool> show_device_state()
    {
        if (sim == IntPtr.Zero || native_api.hpe_gui_device_count(sim).ToUInt64() == 0) {
            show_error("Run a scenario containing virtual devices first.");
            return false;
        }
        var list = new ListBox();
        var dump = new TextBox { IsReadOnly = true, FontFamily = new FontFamily("monospace"),
                                 AcceptsReturn = true, TextWrapping = TextWrapping.Wrap };
        var panel = new Grid { ColumnDefinitions = new ColumnDefinitions("220,*"),
                               Margin = new Thickness(12) };
        panel.Children.Add(list);
        Grid.SetColumn(dump, 1);
        panel.Children.Add(dump);
        var win = new Window { Title = "Device state", Width = 740, Height = 500, Content = panel,
                               WindowStartupLocation = WindowStartupLocation.CenterOwner };
        var n = (int)native_api.hpe_gui_device_count(sim).ToUInt64();
        var devices = new List<native_api.native_device>();
        for (int i = 0; i < n; i++) {
            native_api.hpe_gui_device_get(sim, (UIntPtr)i, out var d);
            devices.Add(d);
            list.Items.Add($"{d.get_name()}  [{protocol_name(d.protocol)}]");
        }
        int selected = -1;
        // Refreshes the visible memory dump for the currently selected device.
        void refresh_memory()
        {
            int i = selected;
            if (i < 0 || sim == IntPtr.Zero) {
                return;
            }
            var d = devices[i];
            var bytes = new byte[Math.Min(512, (int)d.memory_size)];
            int rc =
                native_api.hpe_gui_device_read(sim, (UIntPtr)i, 0, bytes, (UIntPtr)bytes.Length);
            dump.Text =
                rc == 0 ? format_memory(bytes) : $"Unable to read device memory (status {rc}).";
        }
        list.SelectionChanged += (_, _) =>
        {
            selected = list.SelectedIndex;
            refresh_memory();
        };
        var refresh = new DispatcherTimer { Interval = TimeSpan.FromMilliseconds(350) };
        refresh.Tick += (_, _) => refresh_memory();
        refresh.Start();
        win.Closed += (_, _) => refresh.Stop();
        if (hpe_app.smoke_test) {
            var close_timer = new DispatcherTimer { Interval = TimeSpan.FromMilliseconds(250) };
            close_timer.Tick += (_, _) =>
            {
                close_timer.Stop();
                win.Close();
            };
            win.Opened += (_, _) => close_timer.Start();
        }
        if (n > 0) {
            list.SelectedIndex = 0;
        }
        await win.ShowDialog(this);
        return (dump.Text ?? "").StartsWith("Offset", StringComparison.Ordinal);
    }
    /// <summary>Performs the protocol name operation.</summary>
    private static string protocol_name(int p) => p switch {
        1 => "I2C",
        2 => "SPI",
        3 => "SD",
        4 => "eMMC",
        5 => "USB",
        6 => "PCIe",
        _ => "Unknown"
    };
    /// <summary>Performs the format memory operation.</summary>
    private static string format_memory(byte[] bytes)
    {
        var sb = new System.Text.StringBuilder(
            "Offset   00 01 02 03 04 05 06 07 08 09 0A 0B 0C 0D 0E 0F\n");
        for (int i = 0; i < bytes.Length; i += 16) {
            sb.Append($"{i:X6}   ");
            for (int j = 0; j < 16; j++) {
                sb.Append(i + j < bytes.Length ? $"{bytes[i+j]:X2} " : "   ");
            }
            sb.AppendLine();
        }
        return sb.ToString();
    }
    /// <summary>Performs the show error operation.</summary>
    private void show_error(string message)
    {
        var w = new Window {
            Title = "HPE", Width = 460, Height = 180,
            WindowStartupLocation = WindowStartupLocation.CenterOwner,
            Content =
                new StackPanel {
                    Margin = new Thickness(18), Spacing = 12,
                    Children = { new TextBlock { Text = message, TextWrapping = TextWrapping.Wrap },
                                 new Button { Content = "OK",
                                              HorizontalAlignment = HorizontalAlignment.Right } }
                }
        };
        ((Button)((StackPanel)w.Content!).Children[1]).Click += (_, _) => w.Close();
        w.ShowDialog(this);
    }
    /// <summary>Performs the open file operation.</summary>
    private async Task open_file()
    {
        var files = await StorageProvider.OpenFilePickerAsync(
            new FilePickerOpenOptions { AllowMultiple = false,
                                        FileTypeFilter = new[] { new FilePickerFileType(
                                            "HPE scenario") { Patterns = new[] { "*.hpe" } } } });
        if (files.Count > 0) {
            scenario.Text = await File.ReadAllTextAsync(files[0].Path.LocalPath);
        }
    }
    /// <summary>Performs the save file operation.</summary>
    private async Task save_file()
    {
        var f = await StorageProvider.SaveFilePickerAsync(
            new FilePickerSaveOptions { SuggestedFileName = "scenario.hpe",
                                        FileTypeChoices = new[] { new FilePickerFileType(
                                            "HPE scenario") { Patterns = new[] { "*.hpe" } } } });
        if (f != null) {
            await File.WriteAllTextAsync(f.Path.LocalPath, scenario.Text);
        }
    }
    /// <summary>Performs the run smoke test operation.</summary>
    private async void run_smoke_test()
    {
        var report = new List<string>();
        int failures = 0;
        // Appends progress to the persistent smoke-test report.
        void record(string value)
        {
            report.Add(value);
            File.WriteAllLines(Path.Combine(Path.GetTempPath(), "hpe-gui-smoke-test.log"), report);
        }
        // Records one assertion result and tracks failures for the process exit code.
        void check(bool ok, string name)
        {
            report.Add($"{(ok?"PASS":"FAIL")} {name}");
            if (!ok) {
                failures++;
            }
        }
        try {
            string[] files = { "i2c/i2c_basic.hpe", "spi/spi_nor.hpe", "sdmmc/sd_emmc_blocks.hpe",
                               "usb/usb2_echo.hpe", "pcie/pcie_basic.hpe" };
            int[] protocols = { 1, 2, 3, 5, 6 };
            record("SMOKE START");
            for (int i = 0; i < files.Length; i++) {
                record($"START {files[i]}");
                scenario.Text = load_example_file(files[i]);
                record($"LOADED {files[i]}");
                run_scenario();
                record($"RAN {files[i]} events={events.Count}");
                check(events.Count > 0, $"run {files[i]}");
                check(events.Count == 0 || events[^1].protocol == protocols[i],
                      $"protocol trace {files[i]}");
                check(sim != IntPtr.Zero && native_api.hpe_gui_device_count(sim).ToUInt64() > 0,
                      $"virtual device {files[i]}");
            }
            scenario.Text = load_example_file(files[0]);
            run_scenario();
            ulong before = native_api.hpe_gui_device_count(sim).ToUInt64();
            await add_device();
            run_scenario();
            check(native_api.hpe_gui_device_count(sim).ToUInt64() == before + 1,
                  "add virtual device");
            check(native_api.hpe_gui_device_get(sim, (UIntPtr)before, out var device) == 0 &&
                      device.get_name() == "gui_dialog_smoke",
                  "add device dialog and inspect state");
            check(native_api.hpe_gui_device_read(sim, (UIntPtr)before, 0, new byte[16],
                                                 (UIntPtr)16) == 0,
                  "read device memory");
            check(await show_device_state(), "device state window and live memory view");
            language.SelectedIndex = 1;
            check(lang == "zh", "switch Simplified Chinese");
            language.SelectedIndex = 0;
            waveform.radix = 2;
            waveform.zoom(1);
            waveform.fit();
            int row = Math.Min(2, trace_events.Count - 1);
            trace.SelectedIndex = row;
            check(row >= 0 && waveform.cursor_ns == (long)trace_events[row].time,
                  "trace selection follows waveform cursor");
            clear_simulation();
            check(events.Count == 0 && sim == IntPtr.Zero, "clear simulator and trace");
        } catch (Exception e) {
            report.Add("FAIL exception: " + e);
            failures++;
        }
        File.WriteAllLines(Path.Combine(Path.GetTempPath(), "hpe-gui-smoke-test.log"), report);
        var lifetime =
            (Avalonia.Controls.ApplicationLifetimes.IClassicDesktopStyleApplicationLifetime)
                Application.Current!.ApplicationLifetime!;
        lifetime.Shutdown(failures == 0 ? 0 : 1);
    }
}
/// <summary>Represents the waveform view type.</summary>
internal sealed class waveform_view : Control
{
    /// <summary>Gets or sets the bus events drawn in the waveform.</summary>
    public IReadOnlyList<native_api.native_event> events {
        get; set;
    } = Array.Empty<native_api.native_event>();
    /// <summary>Gets or sets the selected time position in nanoseconds.</summary>
    public long cursor_ns { get; set; }
    /// <summary>Gets or sets the number base used for decoded data labels.</summary>
    public int radix { get; set; } = 16;
    /// <summary>Reports changes to the selected waveform cursor position.</summary>
    public Action<long>? cursor_changed { get; set; }
    private double scale = 0.025, offset;
    private Point? pan;
    private static readonly IBrush[] channel_colors = {
        Brush.Parse("#007AFF"), Brush.Parse("#34C759"), Brush.Parse("#FF9500"),
        Brush.Parse("#AF52DE"), Brush.Parse("#FF3B30"), Brush.Parse("#5AC8FA"),
        Brush.Parse("#5856D6"), Brush.Parse("#8E8E93"), Brush.Parse("#00A6A6"),
        Brush.Parse("#C44569")
    };
    /// <summary>Performs the waveform view operation.</summary>
    public waveform_view()
    {
        ClipToBounds = true;
        MinHeight = 410;
        PointerPressed += handle_pointer_pressed;
        PointerMoved += handle_pointer_moved;
        PointerReleased += handle_pointer_released;
        PointerWheelChanged += handle_wheel;
    }
    /// <summary>Performs the Render operation.</summary>
    public override void Render(DrawingContext c)
    {
        base.Render(c);
        var b = Bounds;
        c.FillRectangle(Brushes.White, b);
        c.DrawRectangle(null, new Pen(Brush.Parse("#D9DDE3")), b);
        var ch = channels();
        var data_events = events.Where(e => e.kind is 5 or 10 or 11).ToArray();
        double top = 26, row = 38;
        using (c.PushClip(new Rect(0, 0, b.Width, b.Height)))
        {
            for (int x = 78; x < b.Width; x += 100) {
                double t = (x - 78 + offset) / scale;
                c.DrawLine(new Pen(Brush.Parse("#E8EBEF"), 1), new Point(x, 0),
                           new Point(x, b.Height));
                c.DrawText(new FormattedText($"{t:0}", CultureInfo.InvariantCulture,
                                             FlowDirection.LeftToRight, Typeface.Default, 11,
                                             Brushes.Gray),
                           new Point(x + 2, 4));
            }
            for (int i = 0; i < ch.Count; i++) {
                double y = top + i * row;
                var pen = new Pen(channel_colors[i % channel_colors.Length], 2);
                c.DrawText(new FormattedText(ch[i], CultureInfo.InvariantCulture,
                                             FlowDirection.LeftToRight, Typeface.Default, 12,
                                             Brushes.DarkSlateGray),
                           new Point(5, y + 4));
                c.DrawLine(new Pen(Brush.Parse("#EFF1F4")), new Point(75, y + row - 2),
                           new Point(b.Width, y + row - 2));
                bool? last = null;
                double last_x = 78;
                foreach (var e in events) {
                    bool? value = signal_value(e, ch[i]);
                    if (value is null || value == last) {
                        continue;
                    }
                    double x = 78 + (long)e.time * scale - offset;
                    if (x < 78) {
                        last = value;
                        last_x = x;
                        continue;
                    }
                    if (x > b.Width) {
                        break;
                    }
                    if (last.HasValue) {
                        double yy = last.Value ? y + 8 : y + 25;
                        c.DrawLine(pen, new Point(last_x, yy), new Point(x, yy));
                        c.DrawLine(pen, new Point(x, y + 8), new Point(x, y + 25));
                    }
                    last = value;
                    last_x = x;
                }
                if (last.HasValue) {
                    c.DrawLine(pen, new Point(last_x, last.Value ? y + 8 : y + 25),
                               new Point(b.Width, last.Value ? y + 8 : y + 25));
                }
                if (ch[i] is "SDA" or "MOSI" or "MISO" or "CMD" or "DAT0") {
                    double last_labelX = -100;
                    foreach (var e in data_events) {
                        double x = 78 + (long)e.time * scale - offset;
                        if (x < 78 || x + 42 > b.Width || x - last_labelX < 44) {
                            continue;
                        }
                        string value = radix switch {
                            2 => Convert.ToString(e.value, 2).PadLeft(8, '0'),
                            8 => Convert.ToString(e.value, 8),
                            10 => e.value.ToString(CultureInfo.InvariantCulture),
                            _ => e.value.ToString("X2")
                        };
                        c.DrawText(new FormattedText(value, CultureInfo.InvariantCulture,
                                                     FlowDirection.LeftToRight, Typeface.Default,
                                                     10, Brushes.Black),
                                   new Point(x + 2, y - 3));
                        last_labelX = x;
                    }
                }
            }
            if (cursor_ns >= 0) {
                double x = 78 + cursor_ns * scale - offset;
                if (x >= 78 && x <= b.Width) {
                    c.DrawLine(new Pen(Brushes.LimeGreen, 2), new Point(x, 0),
                               new Point(x, b.Height));
                }
            }
        }
    }
    /// <summary>Performs the channels operation.</summary>
    private List<string> channels()
    {
        if (events.Count == 0) {
            return new() { "SCL / CLK", "SDA / MOSI", "MISO / DAT0" };
        }
        int p = events[^1].protocol;
        return p switch {
            1 => new() { "SCL", "SDA" },
            2 => new() { "SCLK", "MOSI", "MISO", "CS" },
            3 or
            4 => new() { "CLK", "CMD", "DAT0", "DAT1", "DAT2", "DAT3", "DAT4", "DAT5", "DAT6",
                         "DAT7" },
            5 => new() { "USB D+", "USB D−" },
            6 => new() { "REFCLK", "TX", "RX", "LTSSM" },
            _ => new() { "CLK", "DATA" }
        };
    }
    /// <summary>Performs the signal value operation.</summary>
    private static bool? signal_value(native_api.native_event e, string ch) => ch switch {
        "SCL" => e.scl != 0,
        "SDA" => e.sda != 0,
        "SCLK" => e.clock != 0,
        "MOSI" => e.mosi != 0,
        "MISO" => e.miso != 0,
        "CS" => e.chip_select == 0,
        "CLK" => e.clock != 0,
        "CMD" => e.mosi != 0,
        "DAT0" => native_api.get_line(e, 0) != 0,
        "DAT1" => native_api.get_line(e, 1) != 0,
        "DAT2" => native_api.get_line(e, 2) != 0,
        "DAT3" => native_api.get_line(e, 3) != 0,
        "DAT4" => native_api.get_line(e, 4) != 0,
        "DAT5" => native_api.get_line(e, 5) != 0,
        "DAT6" => native_api.get_line(e, 6) != 0,
        "DAT7" => native_api.get_line(e, 7) != 0,
        "USB D+" => e.mosi != 0,
        "USB D−" => e.miso != 0,
        "TX" => e.mosi != 0,
        "RX" => e.miso != 0,
        "REFCLK" => e.clock != 0,
        "LTSSM" => e.value % 2 == 1,
        _ => null
    };
    /// <summary>Performs the zoom operation.</summary>
    public void zoom(int d, double? anchor_x = null)
    {
        double x = anchor_x ?? Bounds.Width / 2;
        double time = (x - 78 + offset) / scale;
        scale = Math.Clamp(scale * (d > 0 ? 1.35 : 1 / 1.35), 0.002, 3);
        offset = Math.Max(0, time * scale - (x - 78));
        InvalidateVisual();
    }
    /// <summary>Performs the fit operation.</summary>
    public void fit()
    {
        offset = 0;
        if (events.Count > 0) {
            long max = (long)events[^1].time;
            scale = Math.Clamp((Bounds.Width - 100) / Math.Max(1, max), 0.002, 3);
        }
        InvalidateVisual();
    }
    /// <summary>Performs the handle wheel operation.</summary>
    private void handle_wheel(object? s, PointerWheelEventArgs e)
    {
        zoom(e.Delta.Y > 0 ? 1 : -1, e.GetPosition(this).X);
        e.Handled = true;
    }
    /// <summary>Performs the handle pointer pressed operation.</summary>
    private void handle_pointer_pressed(object? s, PointerPressedEventArgs e)
    {
        var p = e.GetPosition(this);
        if (e.GetCurrentPoint(this).Properties.IsMiddleButtonPressed) {
            pan = p;
            e.Pointer.Capture(this);
        }
        else if (p.X >= 78) {
            cursor_ns = (long)Math.Max(0, (p.X - 78 + offset) / scale);
            cursor_changed?.Invoke(cursor_ns);
            InvalidateVisual();
        }
    }
    /// <summary>Performs the handle pointer moved operation.</summary>
    private void handle_pointer_moved(object? s, PointerEventArgs e)
    {
        if (pan is Point p) {
            var now = e.GetPosition(this);
            offset = Math.Max(0, offset - (now.X - p.X));
            pan = now;
            InvalidateVisual();
        }
    }
    /// <summary>Performs the handle pointer released operation.</summary>
    private void handle_pointer_released(object? s, PointerReleasedEventArgs e)
    {
        pan = null;
        e.Pointer.Capture(null);
    }
}
