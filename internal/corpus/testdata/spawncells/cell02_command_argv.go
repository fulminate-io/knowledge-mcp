package spawncells

// CELL 2 — exec.Command with extra argv. ARITY varies against cell 1; the
// import is ALIASED here so the alias axis is exercised inside the files
// rather than spending a cell on it.
import ex "os/exec"

func Cell02(spec *Provider) error {
	cmd := ex.Command(spec.GetCommand(), "--serve", "--quiet")
	return cmd.Run()
}
