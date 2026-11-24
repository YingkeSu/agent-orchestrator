package dsh

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// MinimumVersion is the first tagged DeepSeek Harness release whose ACP profile
// AO has verified. Newer releases, including newer prereleases, are accepted.
const MinimumVersion = "0.2.1-rc.2"

// versionPattern accepts the plain `dsh --version` output as well as a prefixed
// form such as "dsh 0.2.1-rc.2". Build metadata is ignored for comparison.
var versionPattern = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])v?(\d+)\.(\d+)\.(\d+)(?:-([0-9a-z.-]+))?(?:\+[0-9a-z.-]+)?`)

// Version is a parsed semantic version. Pre holds the prerelease identifiers in
// order; an empty Pre means a final release, which sorts after every
// prerelease of the same major/minor/patch.
type Version struct {
	Major int
	Minor int
	Patch int
	Pre   []string
}

// String renders the version in canonical form.
func (v Version) String() string {
	base := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) == 0 {
		return base
	}
	return base + "-" + strings.Join(v.Pre, ".")
}

// Less reports whether v sorts before other using semantic-version precedence.
func (v Version) Less(other Version) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor < other.Minor
	}
	if v.Patch != other.Patch {
		return v.Patch < other.Patch
	}
	return comparePrerelease(v.Pre, other.Pre) < 0
}

// ParseVersion extracts a semantic version from output. It reports false when no
// version-shaped token is present.
func ParseVersion(output string) (Version, bool) {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(output))
	if len(match) != 5 {
		return Version{}, false
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return Version{}, false
	}
	minor, err := strconv.Atoi(match[2])
	if err != nil {
		return Version{}, false
	}
	patch, err := strconv.Atoi(match[3])
	if err != nil {
		return Version{}, false
	}
	parsed := Version{Major: major, Minor: minor, Patch: patch}
	if match[4] != "" {
		parsed.Pre = strings.Split(match[4], ".")
	}
	return parsed, true
}

// ValidateVersionOutput checks that output reports at least MinimumVersion.
func ValidateVersionOutput(output string) error {
	installed, ok := ParseVersion(output)
	if !ok {
		return fmt.Errorf("unrecognized dsh version %q (AO requires %s or newer)",
			strings.TrimSpace(output), MinimumVersion)
	}
	minimum, _ := ParseVersion(MinimumVersion)
	if installed.Less(minimum) {
		return fmt.Errorf("dsh %s is older than AO's tested minimum %s", installed, MinimumVersion)
	}
	return nil
}

// ProbeVersion runs `dsh --version` and enforces MinimumVersion.
func ProbeVersion(ctx context.Context, bin string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	output, err := aoprocess.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("read dsh version: %w", err)
	}
	return ValidateVersionOutput(string(output))
}

func comparePrerelease(a, b []string) int {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	// A final release has higher precedence than any prerelease.
	if len(a) == 0 {
		return 1
	}
	if len(b) == 0 {
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if comparison := comparePrereleaseIdentifier(a[i], b[i]); comparison != 0 {
			return comparison
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

func comparePrereleaseIdentifier(a, b string) int {
	aNumeric, aErr := strconv.Atoi(a)
	bNumeric, bErr := strconv.Atoi(b)
	switch {
	case aErr == nil && bErr == nil:
		return compareInts(aNumeric, bNumeric)
	case aErr == nil:
		// Numeric identifiers sort before alphanumeric ones.
		return -1
	case bErr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func compareInts(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
