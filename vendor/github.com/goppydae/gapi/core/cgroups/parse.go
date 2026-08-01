package cgroups

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// LimitError reports a resource limit string that cannot be turned into
// a ResourceSpec field. Errors are data: the caller gets the field, the
// offending value and the reason as fields, and never has to match on a
// formatted message to tell which limit was bad.
type LimitError struct {
	Field  string // "cpu" or "memory"
	Value  string // the limit string as supplied
	Reason string // why it cannot be represented
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("invalid %s limit %q: %s", e.Field, e.Value, e.Reason)
}

// cpuQuotaPeriodUS mirrors the cpu.max period Create writes (see the
// Apply CPU branch). The parser needs it to answer whether a CPU
// quantity can become a quota at all.
const cpuQuotaPeriodUS = 100000

// memUnits maps a normalized (trimmed, upper-cased) suffix to its byte
// multiplier, LONGEST FIRST so that "GB" is tested before "B" and "KB"
// before "K". Order is the whole correctness argument here: the code
// this replaced used strings.TrimRight with the CUTSET "KB", which
// strips every trailing K and B rather than the unit, so "1KB" parsed
// correctly by luck and "1KBB" quietly meant 1024 (GAPI-DIV-049).
var memUnits = []struct {
	suffix string
	mult   int64
}{
	{"GB", 1 << 30},
	{"MB", 1 << 20},
	{"KB", 1 << 10},
	{"B", 1},
	{"G", 1 << 30},
	{"M", 1 << 20},
	{"K", 1 << 10},
}

// ParseResourceSpec converts operator-supplied cpu and memory limit
// strings into a ResourceSpec.
//
// It is the SINGLE source of truth for what a limit string means.
// core/schema validates manifests by calling this function rather than
// reimplementing the formats, so "the manifest was accepted" and "the
// limit can be applied" are one statement instead of two
// implementations that drift apart. That is structural, not a
// convention to remember: core/cgroups imports only internal/safeio, so
// core/schema can depend on it with no cycle.
//
// An empty string means "no limit for this resource": the field stays
// zero and no error is returned, because a manifest that names no limit
// is not an error. Every other unrepresentable input returns a
// *LimitError. What this function must never do - and what
// GAPI-DIV-049 was - is return a zero field with a nil error for a
// non-empty limit: Create writes a limit only when the field is
// positive, so a discarded parse error produced an agent with no
// containment and no log line to say so.
func ParseResourceSpec(cpu, mem string) (ResourceSpec, error) {
	var spec ResourceSpec

	if c := strings.TrimSpace(cpu); c != "" {
		v, err := parseCPU(c)
		if err != nil {
			return ResourceSpec{}, err
		}
		spec.CPU = v
	}

	if m := strings.TrimSpace(mem); m != "" {
		v, err := parseMemory(m)
		if err != nil {
			return ResourceSpec{}, err
		}
		spec.Memory = v
	}

	return spec, nil
}

// parseCPU converts a CPU quantity ("0.5", "1", "500m") to a count of
// cores. limit is already trimmed and non-empty.
func parseCPU(limit string) (float64, error) {
	bad := func(reason string) (float64, error) {
		return 0, &LimitError{Field: "cpu", Value: limit, Reason: reason}
	}

	var v float64
	if millis, ok := strings.CutSuffix(limit, "m"); ok {
		// Millicpu is an INTEGER unit by convention - Kubernetes admits
		// no fractional millicore - and a fraction of a milli would be a
		// millionth of a core, below anything a 100ms quota can express.
		// "0.5m" is therefore rejected rather than rounded. It used to
		// pass validation and convert to zero, which is how this defect
		// was found.
		n, err := strconv.ParseInt(millis, 10, 64)
		if err != nil {
			return bad("millicpu must be a whole number of millicores, as in 500m")
		}
		v = float64(n) / 1000.0
	} else {
		f, err := strconv.ParseFloat(limit, 64)
		if err != nil {
			return bad("not a number (use 0.5, 500m or 1)")
		}
		v = f
	}

	// strconv.ParseFloat accepts "NaN", "Inf" and "+Inf" without error;
	// NaN compares false against every bound and +Inf compares greater
	// than zero, so a plain "v <= 0" test lets all three through
	// (GAPI-DIV-042).
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return bad("not a finite quantity")
	}
	if v <= 0 {
		return bad("must be positive")
	}
	// Create computes quota = period * cores into an int. The memory
	// side of this function rejects counts that overflow their product
	// (GAPI-DIV-042); the CPU side owes the same answer rather than
	// handing the kernel a wrapped quota.
	if v > float64(math.MaxInt)/cpuQuotaPeriodUS {
		return bad("out of range: the cpu.max quota would not fit an int")
	}

	return v, nil
}

// parseMemory converts a memory quantity ("100MB", "1G", "512B") to a
// count of bytes. limit is already trimmed and non-empty.
func parseMemory(limit string) (int64, error) {
	bad := func(reason string) (int64, error) {
		return 0, &LimitError{Field: "memory", Value: limit, Reason: reason}
	}

	upper := strings.ToUpper(limit)

	var num string
	var mult int64
	for _, u := range memUnits {
		if rest, ok := strings.CutSuffix(upper, u.suffix); ok {
			num, mult = rest, u.mult
			break
		}
	}
	// A bare number is REJECTED rather than assumed to be bytes. The
	// unit is what makes a manifest reviewable, and "1024" is one
	// keystroke from meaning a thousand times more than intended; an
	// operator who means bytes writes "1024B", which this function
	// accepts.
	if mult == 0 {
		return bad("no unit (use 100MB, 1GB, 512M or 1024B)")
	}

	v, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return bad("value is not a whole number")
	}
	if v <= 0 {
		return bad("must be positive")
	}
	// A count that fits int64 as gigabytes need not fit as bytes, and
	// the product wraps silently - the cgroup would be handed a NEGATIVE
	// memory limit (GAPI-DIV-042).
	if v > math.MaxInt64/mult {
		return bad(fmt.Sprintf("out of range: the byte count exceeds %d", int64(math.MaxInt64)))
	}

	return v * mult, nil
}
