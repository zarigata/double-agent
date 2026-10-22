package proxmox

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The structs below mirror the Proxmox VE API payloads. The field names are
// the API names, not Go names, because the API is strict about them.

// Version describes the Proxmox VE installation.
type Version struct {
	// Version is the full version of the installation, e.g. "9.2.21".
	Version string `json:"version"`
	// Release is the release of the installation, e.g. "9.2".
	Release string `json:"release"`
	// RepoID identifies the build of the installation.
	RepoID string `json:"repoid"`
}

// NodeStatus is the state of a node of the cluster.
//
// The list of the nodes is the only answer of the server that tells whether
// a node is online, so it is the answer the health check reads.
type NodeStatus struct {
	// Node is the name of the node.
	Node string `json:"node"`
	// Status is "online" or "offline".
	Status string `json:"status"`
	// Type is the kind of the node, e.g. "node".
	Type string `json:"type"`
	// ID is the identifier of the node, e.g. "node/pve".
	ID string `json:"id"`
	// Uptime is the uptime of the node in seconds.
	Uptime int64 `json:"uptime"`
	// MaxCPU is the number of cores of the node.
	MaxCPU int `json:"maxcpu"`
	// MaxMem is the memory of the node in bytes.
	MaxMem int64 `json:"maxmem"`
}

// QemuSummary is one entry of the guest list of a node.
//
// The list is the cheapest way to learn which guests exist on a node, and it
// is the only answer that tells a template apart from a normal guest.
type QemuSummary struct {
	// VMID is the identifier of the guest.
	VMID int `json:"vmid"`
	// Name is the name of the guest.
	Name string `json:"name"`
	// Status is the run state of the guest.
	Status string `json:"status"`
	// Template marks a guest that serves as a template.
	Template Flag `json:"template"`
	// CPU is the usage of the processor of the guest in percent.
	CPU int `json:"cpu"`
	// Cores is the number of cores the guest may use.
	Cores int `json:"cpus"`
	// MaxMem is the memory limit of the guest in bytes.
	MaxMem int64 `json:"maxmem"`
	// Tags are the tags of the guest.
	Tags string `json:"tags"`
}

// QemuConfig is the configuration of a guest.
//
// The configuration is the only place the name of a guest is reported, so it
// is also the place the ownership marker of a guest is read from.
type QemuConfig struct {
	// VMID is the identifier of the guest.
	VMID int `json:"vmid"`
	// Name is the name of the guest.
	Name string `json:"name"`
	// Description is the free text description of the guest.
	Description string `json:"description"`
	// Cores is the number of cores the guest may use.
	Cores Int `json:"cores"`
	// Memory is the memory of the guest in megabytes.
	Memory Int `json:"memory"`
	// SCSIHW is the SCSI controller of the guest.
	SCSIHW string `json:"scsihw"`
	// Net0 is the first network device of the guest.
	Net0 string `json:"net0"`
	// Agent holds the settings of the guest agent.
	Agent Flag `json:"agent"`
	// OSType is the operating system type of the guest.
	OSType string `json:"ostype"`
	// Template marks a guest that serves as a template.
	Template Flag `json:"template"`
	// Tags are the tags of the guest.
	Tags string `json:"tags"`
}

// QemuStatus is the runtime state of a guest.
type QemuStatus struct {
	// VMID is the identifier of the guest.
	VMID int `json:"vmid"`
	// Name is the name of the guest.
	Name string `json:"name"`
	// Status is the run state of the guest, e.g. "running" or "stopped".
	Status string `json:"status"`
	// QMPStatus is the state the QEMU process reports, e.g. "prelaunch".
	QMPStatus string `json:"qmpstatus"`
	// CPUs is the number of cores the guest may use.
	CPUs int `json:"cpus"`
	// MaxMem is the memory limit of the guest in bytes.
	MaxMem int64 `json:"maxmem"`
	// Uptime is the uptime of the guest in seconds.
	Uptime int64 `json:"uptime"`
	// Agent reports whether the guest agent answers.
	Agent int `json:"agent"`
	// PID is the process identifier of the QEMU process.
	PID int `json:"pid"`
}

// TaskStatus is the state of a Proxmox task.
//
// Every write that takes time, such as a clone or a start, answers with the
// identifier of a task. The task has to be polled to learn whether the
// write succeeded.
type TaskStatus struct {
	// UPID is the identifier of the task.
	UPID string `json:"upid"`
	// Node is the node the task runs on.
	Node string `json:"node"`
	// Type is the kind of the task, e.g. "qmclone".
	Type string `json:"type"`
	// ID is the identifier of the object the task works on.
	ID string `json:"id"`
	// User is the user that started the task.
	User string `json:"user"`
	// Status is "running" or "stopped".
	Status string `json:"status"`
	// ExitStatus is the result of the task. A finished task reports "OK".
	ExitStatus string `json:"exitstatus"`
}

// AgentExecRequest is the body of a guest agent exec call.
//
// The API takes the standard input of the command as plain text and encodes
// it itself, and it always asks the guest agent to capture the output. The
// Proxmox API rejects any other property, so the body carries nothing else.
type AgentExecRequest struct {
	// Command is the program and its arguments. It is never handed to a
	// shell, so a recipe cannot smuggle a second command into the guest.
	Command []string `json:"command"`
	// InputData is the data that is fed to the standard input of the
	// command. The API accepts at most 64 KiB.
	InputData string `json:"input-data,omitempty"`
}

// AgentExecStatus is the state of a command that runs inside a guest.
type AgentExecStatus struct {
	// Exited reports whether the command finished.
	Exited Flag `json:"exited"`
	// ExitCode is the exit code of the command.
	ExitCode Int `json:"exitcode"`
	// Signal is the signal that killed the command.
	Signal Int `json:"signal"`
	// OutData is the standard output of the command. Proxmox decodes the
	// base64 of the guest agent, so the text is plain.
	OutData string `json:"out-data"`
	// ErrData is the standard error of the command.
	ErrData string `json:"err-data"`
	// OutTruncated reports whether the guest agent cut the output short.
	OutTruncated Flag `json:"out-truncated"`
}

// CloneRequest describes the guest that is cloned.
//
// The source of the clone is the guest the path of the call names, so it is
// the field SourceVMID that decides what is copied.
type CloneRequest struct {
	// SourceVMID is the guest that is cloned.
	SourceVMID int
	// NewVMID is the identifier of the clone.
	NewVMID int
	// Name is the name of the clone. Proxmox only accepts a DNS name.
	Name string
	// Description is the description of the clone.
	Description string
	// Target is the node the clone is created on.
	Target string
	// Storage is the storage the disks of the clone are placed on.
	Storage string
	// Format is the disk format of the clone. An empty format lets
	// Proxmox pick the format of the storage.
	Format string
	// Full asks for a full copy of the disks of the source.
	//
	// A linked clone shares the disks of its source, which would let a
	// workload damage the template of the operator, so the backend always
	// asks for a full copy.
	Full bool
}

// Flag is a boolean that the Proxmox API encodes as the number 0 or 1.
//
// The API is not consistent here: a list reports a template as the number 1,
// a configuration reports the guest agent as the string "1" and a status
// reports a finished command as the boolean true.
type Flag string

// UnmarshalJSON implements json.Unmarshaler.
func (f *Flag) UnmarshalJSON(data []byte) error {
	value := strings.TrimSpace(string(data))
	switch value {
	case "true":
		*f = "1"
	case "false":
		*f = "0"
	case "null":
		*f = ""
	default:
		*f = Flag(strings.Trim(value, `"`))
	}
	return nil
}

// Enabled reports whether the flag is set.
func (f Flag) Enabled() bool {
	switch strings.ToLower(string(f)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// Int is a number that the Proxmox API returns either as a JSON number or
// as a JSON string, depending on the endpoint.
type Int int

// UnmarshalJSON implements json.Unmarshaler.
func (i *Int) UnmarshalJSON(data []byte) error {
	value := strings.TrimSpace(string(data))
	if value == "null" {
		*i = 0
		return nil
	}
	value = strings.Trim(value, `"`)
	if value == "" {
		*i = 0
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("proxmox: %q is not a number: %w", value, err)
	}
	*i = Int(parsed)
	return nil
}

// MarshalJSON implements json.Marshaler.
func (i Int) MarshalJSON() ([]byte, error) {
	return json.Marshal(int(i))
}
