package spawncells

// CELL 8 — syscall.Exec.
import "syscall"

func Cell08(spec *Provider) error {
	return syscall.Exec(spec.GetCommand(), nil, nil)
}
