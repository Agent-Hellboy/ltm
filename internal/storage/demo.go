package storage

import (
	"fmt"
	"time"

	"ltm/internal/abi"
)

func GenerateDemoEvents(start time.Time, count int) []Event {
	if count <= 0 {
		return nil
	}
	events := make([]Event, 0, count)
	base := start
	paths := []string{"/tmp/ltm-demo.txt", "/tmp/ltm-demo.yaml"}
	for i := 0; i < count; i++ {
		ts := base.Add(time.Duration(i) * 750 * time.Millisecond)
		switch i % 6 {
		case 0:
			events = append(events, Event{
				SchemaVersion: abi.SchemaVersion,
				Timestamp:     ts,
				Category:      "process",
				Action:        "exec",
				PID:           4200 + i,
				PPID:          1,
				UID:           0,
				Comm:          fmt.Sprintf("demo-worker-%d", i),
				Exe:           "/usr/bin/demo-worker",
				Path:          "/usr/bin/demo-worker",
				Metadata:      map[string]any{"scenario": "demo"},
			})
		case 1:
			events = append(events, Event{
				SchemaVersion: abi.SchemaVersion,
				Timestamp:     ts,
				Category:      "file",
				Action:        "write",
				PID:           4200 + i - 1,
				PPID:          1,
				UID:           0,
				Comm:          fmt.Sprintf("demo-worker-%d", i-1),
				Path:          paths[i%len(paths)],
				Metadata:      map[string]any{"bytes": 128},
			})
		case 2:
			events = append(events, Event{
				SchemaVersion: abi.SchemaVersion,
				Timestamp:     ts,
				Category:      "network",
				Action:        "listen",
				PID:           4300,
				PPID:          1,
				UID:           0,
				Comm:          "demo-web",
				LocalAddr:     "127.0.0.1",
				LocalPort:     18080,
			})
		case 3:
			events = append(events, Event{
				SchemaVersion: abi.SchemaVersion,
				Timestamp:     ts,
				Category:      "network",
				Action:        "connect",
				PID:           4310,
				PPID:          4300,
				UID:           1000,
				Comm:          "curl",
				RemoteAddr:    "127.0.0.1",
				RemotePort:    18080,
				RemoteHost:    "localhost",
			})
		case 4:
			events = append(events, Event{
				SchemaVersion: abi.SchemaVersion,
				Timestamp:     ts,
				Category:      "file",
				Action:        "rename",
				PID:           4300,
				PPID:          1,
				UID:           0,
				Comm:          "demo-web",
				Path:          "/tmp/ltm-demo.txt",
				OldPath:       "/tmp/ltm-demo.txt.bak",
			})
		case 5:
			events = append(events, Event{
				SchemaVersion: abi.SchemaVersion,
				Timestamp:     ts,
				Category:      "process",
				Action:        "exit",
				PID:           4300,
				PPID:          1,
				UID:           0,
				Comm:          "demo-web",
				Exe:           "/usr/bin/demo-web",
				ExitCode:      0,
			})
		}
	}
	return events
}
