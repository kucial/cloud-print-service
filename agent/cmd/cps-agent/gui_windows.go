//go:build gui && windows

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "CPS Agent"

// acquireSingleInstance keeps one tray app per signed-in user: two would fight over
// the printer ports. The mutex lives as long as the process.
func acquireSingleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Local\cps-agent-gui`)
	_, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		showError("CPS agent is already running. Look for its icon in the notification area.")
		return false
	}
	return true
}

// serviceBlocks keeps the tray app from starting a second agent when the Windows
// service (the CLI install) already serves this PC. Querying needs no elevation.
func serviceBlocks() string {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return ""
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := windows.UTF16PtrFromString(serviceName)
	h, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return ""
	}
	windows.CloseServiceHandle(h)
	return "The cps-agent Windows service is installed and serves this PC's printers. " +
		"Remove it (cps-agent.exe -uninstall, as Administrator) to use the desktop app instead."
}

func autostartSupported() bool { return true }

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

func setAutostart(on bool, configPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runValue, fmt.Sprintf(`"%s" -config "%s"`, exe, configPath))
}

func showError(msg string) {
	title, _ := windows.UTF16PtrFromString("CPS agent")
	text, _ := windows.UTF16PtrFromString(msg)
	windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONWARNING)
}
