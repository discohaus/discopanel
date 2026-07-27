package runtimespec

import (
	"encoding/json"
	"os"
	"path/filepath"

	agentv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/agent/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Exit files hold the agent Exited message as protojson

const (
	ExitReportFileName  = "last-exit.json"
	ExitHistoryFileName = "exit-history.json"
)

// History ring stays small, loops repeat the same story
const maxExitHistory = 20

func ExitReportPath(dataDir string) string {
	return filepath.Join(dataDir, StateDir, ExitReportFileName)
}

func ExitHistoryPath(dataDir string) string {
	return filepath.Join(dataDir, StateDir, ExitHistoryFileName)
}

// Loads the previous run's exit report, nil if absent
func ReadExitReport(dataDir string) *agentv1.Exited {
	data, err := os.ReadFile(ExitReportPath(dataDir))
	if err != nil {
		return nil
	}
	var r agentv1.Exited
	if protojson.Unmarshal(data, &r) != nil || r.ExitedAtUnixMs == 0 {
		return nil
	}
	return &r
}

func WriteExitReport(dataDir string, r *agentv1.Exited) {
	data, err := protojson.Marshal(r)
	if err != nil {
		return
	}
	_ = os.WriteFile(ExitReportPath(dataDir), data, 0644)
}

// Loads the exit ring, oldest first, empty when absent
func ReadExitHistory(dataDir string) []*agentv1.Exited {
	return readProtoList(ExitHistoryPath(dataDir), func() *agentv1.Exited { return &agentv1.Exited{} })
}

// Appends one exit to the ring, survives panel acks
func AppendExitHistory(dataDir string, r *agentv1.Exited) {
	history := ReadExitHistory(dataDir)
	// Restart replays must not duplicate entries
	for i := range history {
		if history[i].ExitedAtUnixMs == r.ExitedAtUnixMs {
			return
		}
	}
	history = append(history, r)
	if len(history) > maxExitHistory {
		history = history[len(history)-maxExitHistory:]
	}
	if err := os.MkdirAll(filepath.Join(dataDir, StateDir), 0755); err != nil {
		return
	}
	data, err := marshalProtoList(history)
	if err != nil {
		return
	}
	_ = os.WriteFile(ExitHistoryPath(dataDir), data, 0644)
}

// Encodes proto messages as a json array of protojson
func marshalProtoList[M proto.Message](items []M) ([]byte, error) {
	raw := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		data, err := protojson.Marshal(item)
		if err != nil {
			return nil, err
		}
		raw = append(raw, data)
	}
	return json.Marshal(raw)
}

// Decodes a json array of protojson, empty when absent
func readProtoList[M proto.Message](path string, newMsg func() M) []M {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw []json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	items := make([]M, 0, len(raw))
	for _, r := range raw {
		m := newMsg()
		if protojson.Unmarshal(r, m) != nil {
			return nil
		}
		items = append(items, m)
	}
	return items
}
