package spawncells

// CELL 5 — the &exec.Cmd composite literal carrying Path alone.
import "os/exec"

func Cell05(spec *Provider) error {
	cmd := &exec.Cmd{Path: spec.GetCommand()}
	return cmd.Run()
}
