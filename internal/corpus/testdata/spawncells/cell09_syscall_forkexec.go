package spawncells

// CELL 9 — syscall.ForkExec.
import "syscall"

func Cell09(spec *Provider) (int, error) {
	return syscall.ForkExec(spec.GetCommand(), nil, nil)
}
