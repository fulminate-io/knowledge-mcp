package spawncells

// CELL 10 — the POST-CONSTRUCTION assignment. The superseded check for this
// cell carries no flows_to leg at all, so a re-authoring that assumed ten
// uniform legs would have mis-modeled it.
import "os/exec"

func Cell10(spec *Provider) error {
	cmd := &exec.Cmd{}
	cmd.Path = spec.GetCommand()
	return cmd.Run()
}
