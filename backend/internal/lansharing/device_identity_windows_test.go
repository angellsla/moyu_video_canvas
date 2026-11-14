package lansharing

import (
	"context"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsSMBIOSMatchesSystemUUID(t *testing.T) {
	got, err := readSystemUUID()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "(Get-CimInstance Win32_ComputerSystemProduct).UUID")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := command.Output()
	if err != nil {
		t.Fatalf("system UUID cross-check: %v", err)
	}
	if !strings.EqualFold(got, strings.TrimSpace(string(output))) {
		t.Fatal("SMBIOS UUID does not match Windows system UUID")
	}
	documents, err := windows.KnownFolderPath(windows.FOLDERID_Documents, windows.KF_FLAG_DEFAULT)
	if err != nil || documents == "" {
		t.Fatalf("Documents directory resolution: %v", err)
	}
}
