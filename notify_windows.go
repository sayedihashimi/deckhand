//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

const createNoWindow = 0x08000000

// hideWindow keeps a helper process (git, the toast) from getting a console.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
}

// toastAppID is Windows PowerShell's app ID. An unpackaged program needs a
// registered app ID to show toasts, and this one is always present.
const toastAppID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

// showToast shows a Windows toast notification. tag identifies the session,
// so a newer toast for the same session replaces the older one. It runs
// Windows PowerShell (which can load the WinRT toast API) without a window,
// and waits for it to finish.
func showToast(title, body, tag string) error {
	xml := `<toast><visual><binding template="ToastGeneric"><text>` + xmlEscape(title) +
		`</text><text>` + xmlEscape(body) + `</text></binding></visual></toast>`
	script := strings.Join([]string{
		`$ErrorActionPreference = 'Stop'`,
		`[void][Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime]`,
		`[void][Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime]`,
		`$x = New-Object Windows.Data.Xml.Dom.XmlDocument`,
		`$x.LoadXml(` + psQuote(xml) + `)`,
		`$t = [Windows.UI.Notifications.ToastNotification]::new($x)`,
		`$t.Tag = ` + psQuote(tag),
		`$t.Group = 'deckhand'`,
		`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier(` + psQuote(toastAppID) + `).Show($t)`,
	}, "\n")
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodePS(script))
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
