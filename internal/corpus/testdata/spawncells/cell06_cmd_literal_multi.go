package spawncells

// CELL 6 — the same literal carrying MORE than one field. A $$$REST in a
// comma-separated slot must still consume a leading comma, so this is a
// genuinely different cell from 5 rather than a restatement of it.
import "os/exec"

func Cell06(spec *Provider) error {
	cmd := &exec.Cmd{Path: spec.GetCommand(), Dir: "/tmp"}
	return cmd.Run()
}
