package spawncells

// CELL 7 — os.StartProcess, the primitive below os/exec.
import "os"

func Cell07(spec *Provider) (*os.Process, error) {
	return os.StartProcess(spec.GetCommand(), nil, nil)
}
