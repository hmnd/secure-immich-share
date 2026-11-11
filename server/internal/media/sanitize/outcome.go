package sanitize

import (
	"fmt"
	"strings"
)

// Outcome is the result of one source attempt. It is sealed: the
// cases are Verified, Rejected, ToolError and SourceError.
type Outcome interface{ outcome() }

func (Verified) outcome()    {}
func (Rejected) outcome()    {}
func (ToolError) outcome()   {}
func (SourceError) outcome() {}

// Rejected means the verifier found tags or bytes outside the allowlist.
// Tags holds group:tag names only, never values.
type Rejected struct {
	Tags []string
}

func (r Rejected) Error() string { return "rejected: " + strings.Join(r.Tags, ", ") }

// ToolKind classifies a ToolError.
type ToolKind string

const (
	ToolTimeout     ToolKind = "timeout"
	ToolCrash       ToolKind = "crash"
	ToolUnsupported ToolKind = "unsupported"
)

// ToolError means exiftool or ffmpeg failed.
type ToolError struct {
	Kind ToolKind
	Err  error
}

func (e ToolError) Error() string { return fmt.Sprintf("tool %s: %v", e.Kind, e.Err) }

// SourceKind classifies a SourceError.
type SourceKind string

const (
	SourceTooLarge  SourceKind = "too_large"
	SourceNotFound  SourceKind = "not_found"
	SourceForbidden SourceKind = "forbidden"
	SourceUpstream  SourceKind = "upstream"
)

// SourceError means the bytes couldn't be fetched from Immich.
type SourceError struct {
	Kind SourceKind
	Err  error
}

func (e SourceError) Error() string { return fmt.Sprintf("source %s: %v", e.Kind, e.Err) }

// FailReason is why a job ended in cache.Failed.
type FailReason string

const (
	FailRejected FailReason = "rejected"
	FailTool     FailReason = "tool"
	FailSource   FailReason = "source"
	FailUpstream FailReason = "upstream"
)

// Decision is what the job does after a step's outcome.
type Decision struct {
	Commit   bool       // the Verified outcome is committed
	NextStep int        // ≥ 0: try this step next
	Fail     FailReason // non-empty: the job fails
	Evict    bool       // the share is gone; evict it now
}

// Next is the source-chain transition: given the outcome of step i of n,
// decide what happens. It is the only place step outcomes are interpreted.
func Next(i, n int, o Outcome) Decision {
	last := i+1 >= n
	switch o := o.(type) {
	case Verified:
		return Decision{Commit: true, NextStep: -1}
	case Rejected:
		if last {
			return Decision{NextStep: -1, Fail: FailRejected}
		}
		return Decision{NextStep: i + 1}
	case ToolError:
		return Decision{NextStep: -1, Fail: FailTool}
	case SourceError:
		switch o.Kind {
		case SourceTooLarge:
			if last {
				return Decision{NextStep: -1, Fail: FailSource}
			}
			return Decision{NextStep: i + 1}
		case SourceNotFound, SourceForbidden:
			return Decision{NextStep: -1, Fail: FailSource, Evict: true}
		default:
			return Decision{NextStep: -1, Fail: FailUpstream}
		}
	}
	// Only a nil outcome gets here; the job fails like a broken tool.
	return Decision{NextStep: -1, Fail: FailTool}
}
