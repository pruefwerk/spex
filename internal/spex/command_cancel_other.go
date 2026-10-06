//go:build !unix

package spex

import "os/exec"

// Current release platforms are Unix. Other platforms retain CommandContext's
// process cancellation until their process-tree behavior is qualified.
func cancelCommandTree(cmd *exec.Cmd) {}
