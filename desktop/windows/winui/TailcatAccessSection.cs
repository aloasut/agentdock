using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Windows.ApplicationModel.DataTransfer;

namespace AgentDock.ControlPanel;

public sealed partial class SettingsPage
{
    private bool _showTailcatAddress;

    // Tailcat 拨入不依赖 Cloudflare 组件。这一段在组件未安装时也要显示。
    private SectionCard BuildTailcatAccessSection()
    {
        var snapshot = _snapshot;
        var active = string.Equals(snapshot?.TunnelMode, "tailcat", StringComparison.OrdinalIgnoreCase);
        var rows = new StackPanel();
        rows.Children.Add(new TextBlock
        {
            Text = UiText.Get("TailcatDescription"),
            FontSize = 11.5,
            Opacity = 0.62,
            TextWrapping = TextWrapping.Wrap,
            Padding = new Thickness(13, 10, 13, 8)
        });

        var port = new TextBox
        {
            Header = UiText.Get("TailcatPort"),
            Text = snapshot?.TailcatPort > 0 ? snapshot.TailcatPort.ToString() : "80",
            Tag = "tailcat-port",
            Margin = new Thickness(13, 0, 13, 8)
        };
        rows.Children.Add(port);
        var allow = new TextBox
        {
            Header = UiText.Get("TailcatAllow"),
            PlaceholderText = UiText.Get("TailcatAllowHelp"),
            Text = snapshot?.TailcatAllow ?? "",
            AcceptsReturn = true,
            TextWrapping = TextWrapping.Wrap,
            MinHeight = 64,
            Tag = "tailcat-allow",
            Margin = new Thickness(13, 0, 13, 8)
        };
        rows.Children.Add(allow);

        var actions = new StackPanel
        {
            Orientation = Orientation.Horizontal,
            Spacing = 8,
            Margin = new Thickness(13, 0, 13, 8)
        };
        var apply = new Button { Content = UiText.Get("Apply"), Tag = rows };
        apply.Click += ApplyTailcatButton_Click;
        actions.Children.Add(apply);
        var reset = new Button
        {
            Content = UiText.Get("ResetTailcatConnection"),
            IsEnabled = active
        };
        reset.Click += ResetTailcatButton_Click;
        actions.Children.Add(reset);
        rows.Children.Add(actions);

        var address = snapshot?.TailcatAddress ?? "";
        var addressActions = new StackPanel
        {
            Orientation = Orientation.Horizontal,
            Spacing = 8,
            VerticalAlignment = VerticalAlignment.Center
        };
        var toggle = new Button
        {
            Content = _showTailcatAddress ? UiText.Get("Hide") : UiText.Get("Show"),
            IsEnabled = !string.IsNullOrWhiteSpace(address)
        };
        toggle.Click += (_, _) =>
        {
            _showTailcatAddress = !_showTailcatAddress;
            Render("advancedConnection");
        };
        addressActions.Children.Add(toggle);
        var copy = new Button
        {
            Content = UiText.Get("Copy"),
            IsEnabled = !string.IsNullOrWhiteSpace(address)
        };
        copy.Click += (_, _) => CopyText(address);
        addressActions.Children.Add(copy);
        rows.Children.Add(DetailActionRow(
            UiText.Get("TailcatConnectionString"),
            DisplayTailcatAddress(address, snapshot?.TailcatError),
            addressActions
        ));

        return new SectionCard
        {
            Title = UiText.Get("Tailcat"),
            SectionContent = rows
        };
    }

    private string DisplayTailcatAddress(string address, string? error)
    {
        if (!string.IsNullOrWhiteSpace(error))
        {
            return error!;
        }
        if (string.IsNullOrWhiteSpace(address))
        {
            return UiText.Get("WaitingForTailcat");
        }
        return _showTailcatAddress ? address : UiText.Get("TailcatNoPublicMCP");
    }

    private async void ApplyTailcatButton_Click(object sender, RoutedEventArgs e)
    {
        if (_runtime is null || sender is not Button button || button.Tag is not Panel panel) return;
        var portText = FindTagged<TextBox>(panel, "tailcat-port")?.Text.Trim() ?? "";
        var allowText = FindTagged<TextBox>(panel, "tailcat-allow")?.Text ?? "";
        if (!int.TryParse(portText, out var port) || port < 1 || port > 65535)
        {
            _advancedStatus = UiText.Get("TailcatPortInvalid");
            Render("advancedConnection");
            return;
        }

        button.IsEnabled = false;
        try
        {
            await _runtime.SetTunnelModeAsync("tailcat", "", "", port, allowText, true);
            await RefreshAsync();
            _advancedStatus = UiText.Get("Applied");
        }
        catch (Exception ex)
        {
            _advancedStatus = ex.Message;
        }
        Render("advancedConnection");
    }

    private async void ResetTailcatButton_Click(object sender, RoutedEventArgs e)
    {
        if (_runtime is null || sender is not Button button) return;
        _showTailcatAddress = false;
        button.IsEnabled = false;
        try
        {
            await _runtime.ResetTailcatConnectionAsync();
            await RefreshAsync();
            _advancedStatus = UiText.Get("WaitingForTailcat");
        }
        catch (Exception ex)
        {
            _advancedStatus = ex.Message;
        }
        Render("advancedConnection");
    }
}
