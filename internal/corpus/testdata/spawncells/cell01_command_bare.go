package spawncells

// CELL 1 — exec.Command with the command argument alone.
import "os/exec"

func Cell01(spec *Provider) error {
	cmd := exec.Command(spec.GetCommand())
	return cmd.Run()
}
