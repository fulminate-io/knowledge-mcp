package spawncells

// CELL 3 — exec.CommandContext, no extra argv. ARGUMENT POSITION varies
// against cells 1 and 2: the command sits SECOND, behind a context.
import (
	"context"
	"os/exec"
)

func Cell03(ctx context.Context, spec *Provider) error {
	cmd := exec.CommandContext(ctx, spec.GetCommand())
	return cmd.Run()
}
