// File: App.cs
// Purpose: Project-owned GUI component for apps / gui / Hpe.Gui / App.cs.
// Author: Elrond-Xie
// Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.

using Avalonia;
using Avalonia.Controls.ApplicationLifetimes;
using Avalonia.Themes.Fluent;

namespace hpe.gui;

/// <summary>Represents the hpe app type.</summary>
public sealed class hpe_app : Application
{
    /// <summary>Enables the GUI's automated interactive smoke-test flow.</summary>
    public static bool smoke_test { get; set; }
    /// <summary>Performs the Initialize operation.</summary>
    public override void Initialize() => Styles.Add(new FluentTheme());
    /// <summary>Performs the OnFrameworkInitializationCompleted operation.</summary>
    public override void OnFrameworkInitializationCompleted()
    {
        if (ApplicationLifetime is IClassicDesktopStyleApplicationLifetime desktop) {
            desktop.MainWindow = new main_window();
        }
        base.OnFrameworkInitializationCompleted();
    }
}
