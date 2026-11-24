//go:build windows

package commands

import (
	"encoding/binary"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/local/govoid/internal/config"
)

func systemTaskXML(target string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>` + "\n")
	b.WriteString(`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\n")
	b.WriteString("  <RegistrationInfo>\n")
	b.WriteString("    <Description>GoVoid session watch</Description>\n")
	b.WriteString("  </RegistrationInfo>\n")
	b.WriteString("  <Principals>\n")
	b.WriteString("    <Principal id=\"Author\">\n")
	b.WriteString("      <UserId>S-1-5-18</UserId>\n")
	b.WriteString("      <RunLevel>HighestAvailable</RunLevel>\n")
	b.WriteString("    </Principal>\n")
	b.WriteString("  </Principals>\n")
	b.WriteString("  <Settings>\n")
	b.WriteString("    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>\n")
	b.WriteString("    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>\n")
	b.WriteString("    <StartWhenAvailable>true</StartWhenAvailable>\n")
	b.WriteString("    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>\n")
	b.WriteString("    <ExecutionTimeLimit>PT5M</ExecutionTimeLimit>\n")
	b.WriteString("    <AllowHardTerminate>true</AllowHardTerminate>\n")
	b.WriteString("    <Enabled>true</Enabled>\n")
	b.WriteString("  </Settings>\n")
	b.WriteString("  <Triggers>\n")
	b.WriteString("    <BootTrigger>\n")
	b.WriteString("      <Delay>PT15S</Delay>\n")
	b.WriteString("    </BootTrigger>\n")
	b.WriteString("    <TimeTrigger>\n")
	b.WriteString("      <Repetition>\n")
	b.WriteString("        <Interval>PT1M</Interval>\n")
	b.WriteString("        <StopAtDurationEnd>false</StopAtDurationEnd>\n")
	b.WriteString("      </Repetition>\n")
	b.WriteString("      <StartBoundary>2026-01-01T00:00:00</StartBoundary>\n")
	b.WriteString("      <Enabled>true</Enabled>\n")
	b.WriteString("    </TimeTrigger>\n")
	b.WriteString("  </Triggers>\n")
	b.WriteString("  <Actions Context=\"Author\">\n")
	b.WriteString("    <Exec>\n")
	b.WriteString("      <Command>" + xmlEscape(target) + "</Command>\n")
	b.WriteString("      <Arguments>--sessionwatch</Arguments>\n")
	b.WriteString("    </Exec>\n")
	b.WriteString("  </Actions>\n")
	b.WriteString("</Task>\n")
	return b.String()
}

func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return s
	}
	return b.String()
}

func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	buf := make([]byte, 2+len(units)*2)
	buf[0], buf[1] = 0xFF, 0xFE
	for i, unit := range units {
		binary.LittleEndian.PutUint16(buf[2+i*2:], unit)
	}
	return buf
}

func registerSystemTask(target string) error {
	path := filepath.Join(os.TempDir(), "govoid-boot-task.xml")
	if err := os.WriteFile(path, utf16LE(systemTaskXML(target)), 0o600); err != nil {
		return err
	}
	defer os.Remove(path)
	return runSchtasks("/create", "/tn", config.TaskBootName, "/xml", path, "/f")
}
