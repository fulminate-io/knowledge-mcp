package spawncells

// CELL 4 — exec.CommandContext with extra argv, under a grouped aliased
// import.
import (
	"context"
	"fmt"
	ec "os/exec"
)

func Cell04(ctx context.Context, spec *Provider) error {
	fmt.Println("spawning")
	cmd := ec.CommandContext(ctx, spec.GetCommand(), "--serve")
	return cmd.Run()
}
