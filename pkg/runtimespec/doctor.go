package runtimespec

import (
	"os"
	"path/filepath"
	"slices"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Doctor journal on disk is the DoctorState proto as protojson

const DoctorFileName = "doctor.json"

// Installs key on mod id until file sourced
func ActionKey(a *v1.DoctorAction) string {
	if a.Kind == v1.DoctorActionKind_DOCTOR_ACTION_KIND_INSTALL {
		return a.Kind.String() + ":" + a.ModId
	}
	return a.Kind.String() + ":" + a.File
}

func HasTried(inc *v1.DoctorIncident, key string) bool {
	return slices.Contains(inc.Tried, key)
}

func MarkTried(inc *v1.DoctorIncident, key string) {
	if !HasTried(inc, key) {
		inc.Tried = append(inc.Tried, key)
	}
}

// Counts live disables, the budget consumers
func DisabledCount(inc *v1.DoctorIncident) int {
	n := 0
	for _, a := range inc.Actions {
		if a.Kind == v1.DoctorActionKind_DOCTOR_ACTION_KIND_DISABLE && !a.Reverted {
			n++
		}
	}
	return n
}

// Time of the incident's newest activity
func LastActivity(inc *v1.DoctorIncident) *timestamppb.Timestamp {
	last := inc.OpenedAt
	for _, a := range inc.Actions {
		if a.AppliedAt != nil && (last == nil || a.AppliedAt.AsTime().After(last.AsTime())) {
			last = a.AppliedAt
		}
	}
	return last
}

// Files an open incident holds, still on trial
func IncidentHeldFiles(dataPath string) []string {
	j := LoadDoctor(dataPath)
	if j.Incident == nil {
		return nil
	}
	var files []string
	for _, a := range j.Incident.Actions {
		if a.Reverted || a.File == "" {
			continue
		}
		switch a.Kind {
		case v1.DoctorActionKind_DOCTOR_ACTION_KIND_DISABLE:
			files = append(files, a.File)
		case v1.DoctorActionKind_DOCTOR_ACTION_KIND_DISABLE_PACK:
			files = append(files, filepath.Base(a.File))
		}
	}
	return files
}

// Files the doctor permanently excluded from provisioning
func DoctorExcludes(dataPath string) []string {
	return LoadDoctor(dataPath).Excludes
}

func DoctorPath(dataPath string) string {
	return filepath.Join(dataPath, StateDir, DoctorFileName)
}

func LoadDoctor(dataPath string) *v1.DoctorState {
	data, err := os.ReadFile(DoctorPath(dataPath))
	if err != nil {
		return &v1.DoctorState{Version: 1}
	}
	var s v1.DoctorState
	if protojson.Unmarshal(data, &s) != nil {
		return &v1.DoctorState{Version: 1}
	}
	return &s
}

func SaveDoctor(dataPath string, s *v1.DoctorState) error {
	if err := os.MkdirAll(filepath.Join(dataPath, StateDir), 0755); err != nil {
		return err
	}
	data, err := protojson.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(DoctorPath(dataPath), data, 0644)
}

// Findings file holds PerformanceFinding messages as protojson

const FindingsFileName = "findings.json"

func findingsPath(dataPath string) string {
	return filepath.Join(dataPath, StateDir, FindingsFileName)
}

// Reads doctor-published findings, empty when absent
func ReadFindings(dataPath string) []*v1.PerformanceFinding {
	return readProtoList(findingsPath(dataPath), func() *v1.PerformanceFinding { return &v1.PerformanceFinding{} })
}

// Publishes the doctor's current findings for one server
func WriteFindings(dataPath string, findings []*v1.PerformanceFinding) error {
	if err := os.MkdirAll(filepath.Join(dataPath, StateDir), 0755); err != nil {
		return err
	}
	data, err := marshalProtoList(findings)
	if err != nil {
		return err
	}
	return os.WriteFile(findingsPath(dataPath), data, 0644)
}
